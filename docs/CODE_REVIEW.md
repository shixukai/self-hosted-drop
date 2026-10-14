# Code review — self-hosted Drop

Review of the Go API, worker, Caddy routing, PostgreSQL migrations, Compose layout, and React admin. No runtime behavior was changed: nothing found was both a critical, remotely exploitable hole and a trivial safe patch.

Severity here means what an operator should fix first, not a CVSS score. Line numbers refer to the tree this review was written against.

## Critical

None.

These were checked and did not show a remotely exploitable hole:

- Zip slip and path traversal on upload (`os.OpenRoot`, `ValidatePath`, symlink and compression-method rejection, re-inspection outside the worker mount).
- Server-side SSRF. External URLs in HTML, CSS, and JS are warnings only. The app does not fetch them.
- Admin static files. `filepath.Join(webDir, filepath.Clean("/"+urlPath))` collapses `..` against the leading slash before joining, and `http.ServeFile` rejects a `..` segment in the URL. A request cannot escape `DROP_WEB_DIR`.
- Cookie session CSRF for state-changing admin APIs (Origin plus `X-CSRF-Token`). Production cookies use the `__Host-` prefix.
- Preview auth fall-through. In Caddy 2.11.7, `reverse_proxy` `handle_response` continues to `file_server` only for status class `2`. A 401/403 is copied to the client and does not serve the artifact.

## Important

### 1. `publications:write` undoes an emergency pause

`PUBLISH` and `ROLLBACK` are rejected for `DELETED` sites, but not for `PAUSED` sites. The target status defaults to `ACTIVE`, so either action puts a paused site back on the public hostname.

```130:155:internal/app/publications.go
	target := "ACTIVE"
	did := s.DeploymentID
	switch req.Action {
	case "PUBLISH", "ROLLBACK":
		if s.Status == "DELETED" {
			return p, apiError(409, "SITE_DELETED", "请先恢复站点")
		}
		// ... readiness checks ...
		did = &d.ID
	case "PAUSE":
		// ...
		target = "PAUSED"
```

Pause and resume are `sites:manage`. Publish is `publications:write` (`internal/app/auth.go` line 367). The README’s automation token (`sites:read`, `deployments:write`, `publications:write`) can therefore publish over a pause. The admin UI allows the same rollback while paused: `web/src/pages/SiteDetail.tsx` disables that button for `DELETED` only (around lines 421–446), not for `PAUSED`.

`autoPublish` uses the same path (`internal/app/publications.go` lines 213–231). A deployment uploaded with `publishAfterReady` before the pause can still activate the site when processing finishes.

The PRD treats pause as the emergency takedown. As written, pause is sticky only until the next publish.

### 2. One stuck publication blocks every other route change

Accepting a publication is per site:

```124:128:internal/app/publications.go
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM publications WHERE site_id=$1 AND status IN('PENDING','APPLYING','RECOVERING'))", sid).Scan(&active); e != nil {
		return p, e
	}
```

Applying one is global. While any row is `APPLYING` or `RECOVERING`, every other publish job returns “another publication requires recovery” and is retried forever (`internal/app/publications.go` lines 326–331 and `internal/app/jobs.go` lines 99–103). `syncGateway`, GC, and backup refuse the same states (`internal/app/publications.go` lines 536–540, `internal/app/system.go` lines 176–181, `internal/app/backup.go` lines 44–49).

`PUBLISH` jobs never reach the three-attempt failure path, because the retry condition is `kind == "PUBLISH" || attempts < 2`. There is no admin API to abandon a `RECOVERING` row. If `previous_config` is missing, `compensate` calls `recovering` again (`internal/app/publications.go` lines 434–436) and the loop does not end.

Effects:

- Public files keep being served from the last good Caddy config. That part is intentional.
- A pause or delete accepted for any site stays `PENDING` until the wedged publication clears. Preview checks `desired_status` and dies immediately (`internal/app/preview.go` line 143). The public hostname does not.
- `drop doctor` probes `sites.status`, not `desired_status` (`internal/app/backup.go` lines 225–240), so it can print success while a pause has not been applied.

### 3. Idempotency keys pin a failed upload or publish for 7 days

The key is stored before the operation’s outcome is known, and a replay returns that same resource id (`internal/app/deployments.go` lines 214–234). Maintenance deletes rows only after 7 days (`internal/app/jobs.go` line 270).

`scripts/drop-upload.py` line 44 derives the default key from site id, file hash, filename, and metadata. Re-running the script after `FAILED` or a failed publication returns the old row instead of trying again. The browser UI avoids this by sending `crypto.randomUUID()` (`web/src/api.ts` lines 128–132 and 146).

A replay also inserts a new `RECEIVING` deployment before it notices the key (`internal/app/deployments.go` lines 125–134 and 185–194). The deferred cleanup then marks that extra row `FAILED`. The client still receives the original resource.

### 4. Login limiting collapses to the reverse proxy

```76:80:internal/app/auth.go
func (a *App) loginRate(r *http.Request) error {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !a.rate("login-ip:"+host, 10) || !a.rate("login-global", 30) {
		return apiError(429, "RATE_LIMITED", "尝试次数过多，请稍后重试")
```

Compose publishes the admin port on `127.0.0.1` only (`compose.yaml` line 55). The documented `douyacn.com` setup puts a proxy in front of that port (`docs/运行与部署.md` section 3). The app ignores `X-Forwarded-For`, so every browser shares the proxy’s address. Ten login attempts per minute from anyone who can reach the admin host locks the administrator out for the rest of that minute, and an attacker can keep doing that. The limit does not make the password brute-forceable (Argon2id, at most two concurrent hashes, 12-character minimum). It does make admin login easy to deny.

### 5. Caddy admin and the probe listener are unauthenticated inside the container

Every generated config sets the admin API to all interfaces, with no authentication, and embeds the internal key in the preview proxy header:

```238:238:internal/app/publications.go
	opts := gateway.Options{InternalAddress: internal, InternalKey: a.Config.InternalKey, AdminListen: "0.0.0.0:2019"}
```

`internal/gateway/caddy.go` line 73 writes that listen address. `deploy/caddy-bootstrap.json` does the same before the first publish. `GET /config/` therefore returns the internal key to any client that can open port 2019.

The probe server on `:8081` is built with `probe=true`, which skips preview auth (`internal/gateway/caddy.go` lines 63–65). It has a route for every `AVAILABLE` deployment, including unpublished ones. A client that knows `d-<deploymentId>` (the id is in the preview URL) and can reach port 8081 reads the files without the one-time grant.

Compose does not publish 2019 or 8081 (`compose.yaml` lines 55 and 105). `database` and `processing` are `internal` networks; `gateway` is not (`compose.yaml` lines 119–122). Unpublished ports are still reachable from the host via the container IP, and from any other container later attached to `gateway`. The docs already say not to forward these ports. The gap is that the listeners are open on a non-internal network, the key is in the config document, and a mistaken `ports:` entry would bypass preview auth entirely.

The internal key does not by itself skip the preview database checks. It only proves the caller is allowed to hit `InternalRouter`.

### 6. A small failed upload keeps a worst-case quota reservation for 24 hours

The reservation is `MaxUpload + 2*MaxExpanded + 16MiB` (`internal/app/deployments.go` line 92): about 2.2 GiB with the defaults, regardless of the file size. On processing failure, `failDeployment` does not delete it. It keeps the larger of the reservation and the bytes received:

```110:112:internal/app/jobs.go
func (a *App) failDeployment(ctx context.Context, did, code, msg string) {
	a.DB.Exec(ctx, "UPDATE deployments SET status='FAILED',error_code=$2,error_message=$3,updated_at=now() WHERE id=$1 AND status NOT IN('READY','CANCELED')", did, code, msg)
	a.DB.Exec(ctx, "UPDATE quota_reservations SET reserved_bytes=GREATEST(reserved_bytes,(SELECT received_bytes FROM deployments WHERE id=$1)) WHERE deployment_id=$1", did)
```

The row is removed only when maintenance deletes an upload directory that has been `FAILED` or `CANCELED` for 24 hours (`internal/app/jobs.go` lines 271–284). A crash during `RECEIVING` is marked failed after 30 minutes (line 271) and then waits out the same 24 hours. The handler’s own `defer` releases the reservation if the request returns normally. The long hold is the crash and worker-failure path.

Default quota is 20 GiB. Roughly nine abandoned uploads fill it. This matches the note in `docs/运行与部署.md` section 4, but the reservation is the worst-case ceiling, not the stored file.

### 7. `publishAfterReady` can fail without failing the deployment

```227:230:internal/app/publications.go
	_, e := a.acceptPublication(r, d.SiteID, publishRequest{DeploymentID: d.ID, ExpectedGeneration: d.Metadata.ExpectedGeneration, Action: "PUBLISH", ReportHash: report.ReportHash, AcknowledgeWarnings: d.Metadata.WarningsPolicy == "allow"})
	if e != nil {
		a.auditEvent(ctx, u.ID, "AUTO_PUBLISH", d.SiteID, d.ID, "NOT_APPLIED")
	}
	return nil
```

A generation conflict, a revoked token, or a paused-site race leaves the deployment `READY`, the job `DONE`, and no publication. The only signal is an audit row. `scripts/drop-upload.py` does not use this flag; it publishes in a second call and checks `APPLIED`. API clients that set `publishAfterReady` can report success after the upload and never notice the miss. `expectedGeneration` is captured at upload time, so a concurrent publish makes this likely.

### 8. Backup verification blocks garbage collection for the whole repository read

`Backup` releases the publication lock after `pg_dump` (`internal/app/backup.go` lines 128–130) but holds lock `82943006` until `restic check --read-data` finishes (lines 187–191). `collect` needs that lock (`internal/app/system.go` lines 165–170). A large repository can sit in `--read-data` for a long time, up to the two-hour timeout, while failed uploads continue to hold quota (item 6) and old releases cannot be removed.

## Minor

### Sessions survive a new login

`login` inserts a session and does not delete older ones (`internal/app/auth.go` lines 229–237). Idle time is 30 minutes and the absolute lifetime is 12 hours. Password change and `reset-password` do delete every session (lines 355–356 and 477–478). A stolen cookie keeps working after the administrator logs in again, until it idles out or the password changes.

### The setup key stays valid after initialization

`POST /api/v1/setup` only checks that `administrators` is empty (`internal/app/auth.go` lines 165–184). The key is still loaded into the process environment on every start (`deploy/entrypoint.sh` lines 6–16). After a successful setup it cannot create a second admin, but it is not rotated or destroyed. Anyone who later obtains the key and can empty that table (restored database, SQL access) can set a new password. The key also remains visible in the server process environment for the life of the container.

### Export is a cookie-authenticated GET with a side effect

`GET /deployments/{id}/export` inserts an artifact pin and streams a zip (`internal/app/deployments.go` lines 402–431). The session middleware checks Origin and CSRF only when the method is not `GET` or `HEAD` (`internal/app/auth.go` lines 280–288). The admin UI uses a plain link (`web/src/pages/SiteDetail.tsx` lines 456–459).

`drop-admin.douyacn.com` and `{slug}.drop.douyacn.com` are same-site under `douyacn.com`. A page the operator published can top-level-navigate the operator’s browser to the export URL if it knows the deployment id. The browser will attach the `SameSite=Lax` session cookie. There is no CORS header, so script on the content origin cannot read the zip. The practical effect is an unwanted download and a 30-minute pin, not a cross-origin data leak. New state-changing GETs would not be covered by the current CSRF check.

### Development cookies are not `__Host-` cookies

Production session and preview cookies are `__Host-`, `Secure`, `HttpOnly`, `Path=/`, and have no `Domain` (`internal/app/auth.go` lines 116–129, covered by `internal/app/auth_test.go`). Development mode uses `drop_session_dev` and `drop_preview_dev` without `Secure`. That is required for `http://*.drop.localhost`. A sibling host that the browser allows to set `Domain=drop.localhost` can plant a cookie of the same name. It still has to be a real server-side session to pass preview auth, so this is a dev-only confusion risk, not a production bypass.

`DROP_DEVELOPMENT_MODE` defaults to `true` in `compose.yaml` line 43 and `.env.example`. That is safe only because `config.Load` rejects a development origin that is not loopback or `.localhost` (`internal/config/config.go` lines 82–91) and rejects an admin host that sits on the content suffix (lines 93–95). Production must set all three of `DROP_ADMIN_ORIGIN=https://drop-admin.douyacn.com`, `DROP_CONTENT_DOMAIN=drop.douyacn.com`, and `DROP_DEVELOPMENT_MODE=false`. With development mode off, both the admin and the content names need HTTPS or the `Secure` cookies will not stick. Failure is closed (login or preview looks broken) rather than downgrading the cookie.

### Root dotfiles can miss the Caddy hide rule

Upload rejection covers a fixed sensitive-name list, not every dotfile (`internal/archive/archive.go` lines 101–104). The gateway then hides `/_drop/*`, `*/.*`, and `*.drop-etag` (`internal/gateway/caddy.go` line 35). Caddy’s `*` does not match across `/`, and `*/.*` does not describe a single segment at the root. A root file such as `.hidden.html` is an allowed extension and may be served on the public or preview host. Nested `.git` and `.env*` names are still rejected at extract time.

### `DAC_OVERRIDE` remains on the long-running processes

The entrypoint reads Docker secrets as root, then `su-exec`s to uid 10001 (`deploy/entrypoint.sh`). The app and worker still add `DAC_OVERRIDE` (`compose.yaml` lines 61–62 and 84–85). After the secrets are in the environment, that capability lets the process read root-owned files in the container (including the secret mounts, which stay on `/run/secrets`). It matters only if another bug becomes a file read. It is not required for normal publishes.

### The dev proxy rewrites `Origin`

`web/vite.config.ts` lines 5–12 set `Origin: http://localhost:18180` on every proxied API call and bind the dev server to `127.0.0.1`. Local HMR does not exercise origin rejection. Production does not use this proxy.

### Worker failures are hard to diagnose

The worker child prints a fixed stderr line and returns a generic `PROCESSING_FAILED` for unexpected errors (`cmd/drop-worker/main.go` lines 132–142). The parent logs `worker status` or the mismatch string, not the extract error. Retry behavior is fine; incident debugging is not.

## Nit

- `passwordHash` ignores the error from `rand.Read` (`internal/app/auth.go` lines 94–96). A failure would hash with a zero salt. `id()` panics on the same error, which is the better behavior.
- `strings.Split(host, ":")[0]` is used for Caddy host matchers and preview host parsing (`internal/gateway/caddy.go` line 68, `internal/app/preview.go` line 85). An IPv6 host would be truncated. Production content domains are DNS names, and development mode is `.localhost`.
- Token list responses show `raw[:13]` (`drop_` plus 8 hex characters) at `internal/app/auth.go` line 397. The secret is still 256 bits. The prefix is enough to recognize a token in logs and UI.
- Site search builds `ILIKE '%' || $2 || '%'` (`internal/app/sites.go` line 90). `%` and `_` in the query are wildcards. The query is parameterized, so this is not SQL injection.
- Audit rows do not store the client address (`internal/app/app.go` lines 171–175). Login denials are harder to trace once everything appears to come from the proxy.
- Postgres is configured with `sslmode=disable` (`internal/config/config.go` line 101). The database network is internal and only the app joins it. Acceptable for this Compose layout; not acceptable if that network is ever shared.
- The CSRF value is copied into module state in an effect (`web/src/main.tsx` lines 342–344). A write in the same turn as the first authenticated paint can go out with an empty token and get a 403. Refresh fixes it.

## Checked and sound

**Authentication.** Argon2id (`m=65536,t=3,p=2`) with a 16-byte salt, constant-time compare, a two-slot hash gate, and a 12–256 character password. Session cookies are `HttpOnly`. The absolute lifetime is 12 hours and the idle timeout is enforced in the `UPDATE ... WHERE touched_at` predicate. API tokens are stored as SHA-256 digests, scoped, bound to sites, and revocable. Bearer requests skip CSRF on purpose. Unknown token scopes are rejected.

**CSRF and browsers.** Writes require `Origin == DROP_ADMIN_ORIGIN` and `X-CSRF-Token == SHA-256("csrf:"+session)`. The React client sends both (`web/src/api.ts` lines 105–116 and 145). There is no CORS allow-list, so a content page cannot read admin JSON. JSON bodies are not a simple form POST. The admin CSP is `script-src 'self'` with no `unsafe-eval` (`internal/app/app.go` line 165). User HTML, filenames, warnings, and descriptions are rendered as React text. There is no `dangerouslySetInnerHTML` under `web/src`.

**Uploads.** HTML and zip only. Paths reject `..`, absolute paths, backslashes, controls, and a sensitive-name list. Symlinks and non-regular zip entries are rejected. Declared and actual sizes are both limited. The worker sees only the `uploads` subpath, wipes its child environment, and the app re-runs `Inspect` under `quarantine` and compares `ReportHash` before renaming into `releases` (`cmd/drop-worker/main.go` lines 97–98, `internal/app/jobs.go` lines 171–183).

**Preview.** The grant is a 256-bit token in the URL fragment, redeemed once, and exchanged only when `Origin` is that preview host. The cookie is host-only. Caddy sends every other preview request through `/_internal/preview-auth` before `file_server`. Pause and delete revoke grants in the same transaction as the intent, and auth also checks `desired_status`.

**Routing.** Slugs cannot start with `d-` or use `api`, `www`, `admin`, or `drop`, so they do not collide with preview hosts. Unknown hosts get a static 404 with no file root. The admin router 404s unless `Host` equals `DROP_ADMIN_ORIGIN`. Content and admin suffixes are rejected at startup when they overlap.

**Frontend upload progress.** Directory packing reports progress from the zip worker, and the multipart body uses `xhr.upload.onprogress` (`web/src/pages/UploadPage.tsx` lines 475–483, `web/src/api.ts` lines 149–152). The browser publish path re-reads `expectedGeneration` immediately before publish (`UploadPage.tsx` lines 271–278).

**Migrations and restore.** `schema_migrations` updates are transactional and serialized with an advisory lock (`db/migrations/migrate.go`). `scripts/restore.py` refuses an existing target directory and a non-empty database, checks artifact hashes, and does not point the live gateway at the restored tree.

**Compose defaults.** Admin and content ports bind to `127.0.0.1`. Postgres has no published port. The worker has no database credentials and no release mount. Root filesystems are read-only, capabilities are dropped, and `no-new-privileges` is set. Those defaults are in good shape. The items in section 5 are the exceptions.

## Suggested order of work

1. Reject `PUBLISH` and `ROLLBACK` while the site is `PAUSED` or `desired_status` is `PAUSED`, or require `sites:manage` for that transition. Keep emergency pause from losing to CI.
2. Add an operator way to finish or abandon a single `RECOVERING` publication, and make `doctor` report desired-versus-applied drift. Do not let one site stall pause for every site.
3. Treat idempotency keys as “this attempt” only while the resource is in progress, or teach `drop-upload.py` to use a fresh key after `FAILED`.
4. Rate-limit login on a proxy-aware key (a single trusted hop, not the raw forwarded header) and keep the in-process ceiling.
5. Put Caddy admin and port 8081 on an internal network, or replace the open admin listener with a Unix socket or mutual TLS. Do not publish 8081.
6. Release or shrink quota reservations when a deployment becomes `FAILED` or `CANCELED`, instead of waiting 24 hours at the worst-case size.
7. Surface `publishAfterReady` conflicts on the deployment or as a failed publication instead of a swallowed audit event.
