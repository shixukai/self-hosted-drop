# Code review — self-hosted Drop

## Contents

- [1. Scope and critical findings](#1-scope-and-critical-findings)
- [2. Important findings](#2-important-findings)
- [3. Minor findings and validation gaps](#3-minor-findings-and-validation-gaps)
- [4. Nits](#4-nits)
- [5. Checked and sound](#5-checked-and-sound)
- [6. Suggested order of work](#6-suggested-order-of-work)

## 1. Scope and critical findings

Review of the Go API, worker, Caddy routing, PostgreSQL migrations, Compose layout, and React admin. Severity means what an operator should address first, not a CVSS score. Findings describe the current source and documented deployment; assumptions and untested risks are identified separately. Function names remain the reference when line numbers change.

No critical remote exploit was established within this review's scope. This does not replace production acceptance or fault injection.

These were checked and did not show a remotely exploitable hole:

- Zip slip and path traversal on upload (`os.OpenRoot`, `ValidatePath`, symlink and compression-method rejection, re-inspection outside the worker mount).
- Server-side SSRF. External URLs in HTML, CSS, and JS are warnings only. The app does not fetch them.
- Admin static files. `filepath.Join(webDir, filepath.Clean("/"+urlPath))` collapses `..` against the leading slash before joining, and `http.ServeFile` rejects a `..` segment in the URL. A request cannot escape `DROP_WEB_DIR`.
- Cookie session CSRF for state-changing admin APIs (Origin plus `X-CSRF-Token`). Production cookies use the `__Host-` prefix.
- Preview auth fall-through. In Caddy 2.11.7, `reverse_proxy` `handle_response` continues to `file_server` only for status class `2`. A 401/403 is copied to the client and does not serve the artifact.

## 2. Important findings

### 2.1 Emergency pause requires explicit resume

`validatePublicationState` rejects PUBLISH and ROLLBACK with `SITE_PAUSED` if either the applied or desired site status is PAUSED. Manual publish, rollback, and automatic publication use this shared acceptance path. Publish permission alone cannot reactivate the paused site; explicit RESUME requires `sites:manage`. The admin UI disables publish and rollback while a pause is applied or pending.

RESUME can explicitly select a same-site READY/AVAILABLE deployment, with the matching inspection report hash and warning acknowledgement. This repairs paused/restored drafts with no prior public artifact, or a paused site whose previous artifact is unusable. Omitting that selection retains the existing requirement for an available previously applied deployment. Paused READY history rows provide the separately confirmed “以此版本恢复服务” action; publication-only tokens remain unable to make this transition.

Generation CAS remains a separate guard. An upload that captured generation `g` before an accepted pause cannot overwrite that pause, because acceptance increments generation to `g+1`. A request using the paused site's fresh generation also encounters the state guard. Uploading and inspecting a replacement artifact remains possible; READY by itself does not restore public service.

### 2.2 Recovery remains global and has a verified maintenance path

Accepting a publication is per site:

```go
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM publications WHERE site_id=$1 AND status IN('PENDING','APPLYING','RECOVERING'))", sid).Scan(&active); e != nil {
		return p, e
	}
```

Applying one is global. While any row is `APPLYING` or `RECOVERING`, other publication jobs return “another publication requires recovery” and remain retryable. `syncGateway`, release GC, and backup refuse the same states. This protects compensation, which restores a full gateway snapshot: applying another site's change before compensation finishes could cause that change to be overwritten by the snapshot.

Ordinary automatic recovery also requires the exact full target configuration, integrity of every active artifact, every site's entry body/ETag or disabled response, and a final configuration read before APPLIED. Compensation verifies the full applied baseline's files and content before recording FAILED. A correct target route alone cannot hide unrelated configuration, content, or asset drift.

`PUBLISH` jobs remain retryable rather than entering the three-attempt failure path. If compensation lacks `previous_config`, it cannot prove the old route and remains `RECOVERING`. The host maintenance command `drop reconcile <publication-id>` reads state and the actual gateway config hash. Mutations require that observed hash, nonblocking coordinator/acceptance/GC locks, artifact verification, exact full-configuration comparison, and content/ETag or disabled-response checks across the sites.

`--mode complete` confirms an observed full target configuration and refuses a publication already marked for compensation. `--mode restore-applied` rebuilds the database-applied baseline even when the old snapshot is missing, applies it with Caddy If-Match, then verifies full readback and responses before marking the target FAILED and completing its task. Other PENDING operations are preserved. A stale hash, a second uncertain publication, or unverifiable files/configuration keeps mutation blocked. Operational instructions are in [运行与部署 4.2](运行与部署.md#42-未知发布状态的核验与恢复); no web recovery endpoint bypasses this maintenance path.

The selected mutation mode persists as `operatorMode` before verification completes. Background jobs preserve that operator decision and the global block. Inspect exposes target/applied configuration-match booleans and the target action/status/generation/deployment, without verifying files or public responses (`verifiedSites` is 0). After a fresh inspect and hash-bound decision, complete can fall back to restore-applied; restore-applied cannot switch to complete while compensation remains selected. Verified commit clears the operator mode. Real fault recovery and external gateway changes still need production acceptance.

Effects:

- Caddy keeps serving its last accepted config. During an unknown result, it can contain the previous or candidate route; the database's applied state alone does not establish which one is live.
- A pause or delete accepted for any site stays `PENDING` until the wedged publication clears. Preview checks `desired_status` and dies immediately (`internal/app/preview.go`). The public hostname does not.
- `drop doctor` reports pending publications and desired/applied status or deployment drift with a nonzero exit code. A terminal failed intent can advance generation without being unhealthy when desired and applied state agree.

### 2.3 A terminal processing failure needs an explicit new attempt key

Upload idempotency is stored in the transaction that accepts the completed payload and queues processing. A matching replay returns the same resource, including its terminal outcome; records expire after seven days. An interrupted `RECEIVING` request that never commits acceptance does not create this record, so its key is not pinned to that failed partial upload.

`scripts/drop-upload.py` derives its upload key from site id, file hash, filename, and metadata. Re-running the same input after a committed upload reaches FAILED or CANCELED returns that terminal deployment. `--retry-failed` explicitly permits one fresh upload attempt only after the script observes one of those terminal statuses; it cannot be combined with `--idempotency-key` and does not retry unknown/in-progress results or publication failure. An operator can also provide a distinct explicit key after confirmed failure. Preserve the returned key if the new attempt's result is unknown; replay must remain stable even after completion.

Publication behaves differently: on each invocation the script reads the current site generation and includes it in its publication key. Acceptance increments generation, so a later invocation after a confirmed publication failure can use the changed generation and a different publication key while reusing the READY deployment. Explicit API reuse of a publication key still returns that key's original outcome.

A matching upload replay fully receives and verifies the request fingerprint before returning the original resource. Its temporary RECEIVING row, reservation, and scratch directory are removed on a successful match, so completed replay leaves no extra failed-history item. A different request under the same key remains an idempotency mismatch; filesystem cleanup failure preserves its reservation until safe reconciliation.

### 2.4 Login has a trusted proxy boundary and a shared resource ceiling

`DROP_TRUSTED_PROXY_CIDRS` defaults to empty, so forwarded addresses are ignored until the direct peer is explicitly trusted. The allowlist accepts exact IPs or CIDRs and rejects all-address `/0` entries. `loginClientAddress` walks X-Forwarded-For from the verified peer backwards to the first untrusted hop; malformed chains fall back to the peer address. The deployed proxy must append the real client address or overwrite untrusted input, and the allowlist must identify the peer as the app actually sees it, including Docker NAT.

The per-client limit remains 10 attempts per minute and the process-wide ceiling remains 30. With a correct proxy allowlist, unrelated clients no longer share the proxy's per-IP bucket; the global ceiling still allows an attacker who can reach the admin entry to consume shared login capacity. Argon2id and the two-slot hash gate protect hashing resources, not login availability. Production access control and entry throttling remain part of deployment acceptance.

### 2.5 Caddy admin and the probe listener require a trusted private network

Every generated config sets the admin API to all interfaces, with no authentication, and embeds the internal key in the preview proxy header:

```go
	opts := gateway.Options{InternalAddress: internal, InternalKey: a.Config.InternalKey, AdminListen: "0.0.0.0:2019"}
```

`internal/gateway/caddy.go` writes that listen address. `deploy/caddy-bootstrap.json` does the same before the first publish. `GET /config/` therefore returns the internal key to any client that can open port 2019.

The probe server on `:8081` is built with `probe=true`, which skips preview auth (`internal/gateway/caddy.go`). It has a route for every `AVAILABLE` deployment, including unpublished ones. A client that knows `d-<deploymentId>` (the id is in the preview URL) and can reach port 8081 reads the files without the one-time grant.

Compose does not publish 2019 or 8081, and the two published HTTP ports bind to host loopback. This is a private-listener trust boundary, not demonstrated direct public exposure. Other containers attached to `gateway` can reach these listeners. On native Linux Docker, the host can generally reach bridge container addresses; Docker Desktop on macOS routes container traffic through a Linux VM and has a different host access path. See [Docker bridge networking](https://docs.docker.com/engine/network/drivers/bridge/) and [Docker Desktop networking](https://docs.docker.com/desktop/features/networking/). An accidental publication or external forwarding of 2019 or 8081 would expose privileged configuration or bypass preview authorization.

Setting `gateway.internal: true` alone does not prevent access by the host or same-network containers, as the [Docker internal-network contract](https://docs.docker.com/reference/cli/docker/network/create/#network-internal-mode---internal) confirms. It would also remove the app's only non-internal network and therefore its current egress path for a remote Restic repository. Prefer an explicitly designed trust boundary, such as a restricted control network plus a separate app egress network, or authenticated/socket-based admin access; verify remote backup connectivity for any network change. These are proposals, not implemented guarantees.

The internal key does not by itself skip the preview database checks. It only proves the caller is allowed to hit `InternalRouter`.

### 2.6 Terminal upload reservations shrink after verified quiescence

Initial upload reservation remains `MaxUpload + 2*MaxExpanded + 16MiB`, about 2.2 GiB with defaults. After FAILED or CANCELED has remained unchanged for at least 150 seconds and no PENDING/RUNNING PROCESS job exists, maintenance can replace that ceiling with measured retained storage. It acquires nonblocking GC and quota locks, locks and rechecks the deployment, then counts the upload directory, both task-owned quarantine prefixes, and an unavailable release tree.

Accounting uses the greater of logical size and allocated file blocks, plus metadata allowance for every file/directory. Symlinks, special files, scan errors, active work, or lock contention preserve the existing reservation. It does not estimate storage from `received_bytes` or release quota while owned trees remain.

Original uploads retain their 24-hour cleanup policy. Deleting that directory does not discard accounting for quarantine or release residue. Backup's GC lock can delay remeasurement/shrink; upload-directory cleanup itself uses the quota/deployment guards independently of that lock. Capacity remains constrained by actual retained bytes, other stored versions, the disk reserve, and any accounting that could not safely be refreshed.

### 2.7 Automatic publication has an outcome separate from readiness

Deployment responses expose `autoPublicationId`, `autoPublishErrorCode`, and `autoPublishErrorMessage`. Accepted automatic publication links its Publication; semantic refusals such as stale generation, pause, revoked/forbidden token, or unacknowledged warnings record a durable separate error and audit while the artifact stays READY. READY proves the artifact, not public routing. Clients must poll the linked Publication through APPLIED or inspect the separate refusal.

Transient database or result-persistence errors remain retryable. A PROCESS job whose artifact is READY but has no durable automatic-publication result retries beyond the normal three processing attempts. If acceptance committed before the result write failed, the automatic idempotency record lets retry link the original Publication instead of accepting a duplicate. `scripts/drop-upload.py` uses a separate publication call and checks APPLIED.

### 2.8 Backup verification delays old-release collection

`Backup` releases the publication lock after `pg_dump` but holds GC lock `82943006` until `restic check --read-data` finishes. `collect` needs that lock, so old release files cannot be removed during the full repository read, which can run up to the two-hour timeout. FAILED/CANCELED upload-directory cleanup does not take the backup GC lock; conservative residual reservation remeasurement does. Capacity planning must distinguish retained releases, actual failed-upload bytes, and accounting refresh delayed by the backup.

## 3. Minor findings and validation gaps

### 3.1 Sessions survive a new login

`login` inserts a session and does not delete older ones (`internal/app/auth.go`). Idle time is 30 minutes and the absolute lifetime is 12 hours. Password change and `reset-password` do delete every session. A stolen cookie keeps working after the administrator logs in again, until it idles out or the password changes.

### 3.2 The setup key stays valid after initialization

`POST /api/v1/setup` only checks that `administrators` is empty (`internal/app/auth.go`). The key is still loaded into the process environment on every start (`deploy/entrypoint.sh`). After a successful setup it cannot create a second admin, but it is not rotated or destroyed. Anyone who later obtains the key and can empty that table (restored database, SQL access) can set a new password. The key also remains visible in the server process environment for the life of the container.

### 3.3 Export is a cookie-authenticated GET with a temporary side effect

`GET /deployments/{id}/export` inserts an artifact pin and streams a zip (`internal/app/deployments.go`). The session middleware checks Origin and CSRF only when the method is not `GET` or `HEAD` (`internal/app/auth.go`). The admin UI uses a plain link (`web/src/pages/SiteDetail.tsx`).

`drop-admin.douyacn.com` and `{slug}.drop.douyacn.com` are same-site under `douyacn.com`. A page the operator published can top-level-navigate the operator's browser to the export URL if it knows the deployment id. The browser can attach the `SameSite=Lax` session cookie. There is no CORS header, so script on the content origin cannot read the zip. The practical effect is an unwanted download and a pin for the request's lifetime. `exportDeployment` deletes the pin on return, including a normal client disconnect; the 30-minute expiry is a fallback for process interruption or failed cleanup. No cross-origin data read was established. New state-changing GETs would not be covered by the current CSRF check.

### 3.4 Development cookies are not `__Host-` cookies

Production session and preview cookies are `__Host-`, `Secure`, `HttpOnly`, `Path=/`, and have no `Domain` (`internal/app/auth.go`, covered by `internal/app/auth_test.go`). Development mode uses `drop_session_dev` and `drop_preview_dev` without `Secure`. That is required for `http://*.drop.localhost`. A sibling host that the browser allows to set `Domain=drop.localhost` can plant a cookie of the same name. It still has to be a real server-side session to pass preview auth, so this is a dev-only confusion risk, not a production bypass.

`DROP_DEVELOPMENT_MODE` defaults to `true` in `compose.yaml` and `.env.example`. That is safe only because `config.Load` rejects a development origin that is not loopback or `.localhost` (`internal/config/config.go`) and rejects an admin host that sits on the content suffix. Production must set all three of `DROP_ADMIN_ORIGIN=https://drop-admin.douyacn.com`, `DROP_CONTENT_DOMAIN=drop.douyacn.com`, and `DROP_DEVELOPMENT_MODE=false`. With development mode off, both the admin and the content names need HTTPS or the `Secure` cookies will not stick. Failure is closed (login or preview looks broken) rather than downgrading the cookie.

### 3.5 A dotfile selected as the entry needs a focused runtime check

Upload rejection covers a fixed sensitive-name list, not every dotfile. The gateway's URL-path deny matcher includes `*/.*`. In Caddy 2.11.7, a pattern with leading and trailing `*` matches the contained substring; `*/.*` therefore matches `/.` anywhere in the requested path, including `/.hidden.html` at the root. Direct root or nested dotfile URL exposure is not supported by the [versioned MatchPath semantics](https://pkg.go.dev/github.com/caddyserver/caddy/v2@v2.11.7/modules/caddyhttp#MatchPath).

Source-level risk, unverified at runtime: `fileHandlers` checks the URL-path deny matcher before rewriting `/` or an SPA fallback to the selected entry, while the final file-server hide list only covers ETag sidecars. If an allowed dot-prefixed HTML file can be selected as the entry, the rewrite may reach it without repeating that path matcher. Verify explicit entry selection and GET/HEAD/SPA fallback on Caddy before deciding whether to reject dotfile entries or add a filesystem hide rule. This is a different path from direct dotfile requests.

### 3.6 The dev proxy rewrites `Origin`

`web/vite.config.ts` set `Origin: http://localhost:18180` on every proxied API call and bind the dev server to `127.0.0.1`. Local HMR does not exercise origin rejection. Production does not use this proxy.

### 3.7 Worker failures are hard to diagnose

The worker child prints a fixed stderr line and returns a generic `PROCESSING_FAILED` for unexpected errors (`cmd/drop-worker/main.go`). The parent logs `worker status` or the mismatch string, not the extract error. Retry behavior is fine; incident debugging is not.

## 4. Nits

- `strings.Split(host, ":")[0]` is used for Caddy host matchers and preview host parsing (`internal/gateway/caddy.go`, `internal/app/preview.go`). An IPv6 host would be truncated. Production content domains are DNS names, and development mode is `.localhost`.
- Token list responses show `raw[:13]` (`drop_` plus 8 hex characters) at `internal/app/auth.go`. The secret is still 256 bits. The prefix is enough to recognize a token in logs and UI.
- Site search builds `ILIKE '%' || $2 || '%'` (`internal/app/sites.go`). `%` and `_` in the query are wildcards. The query is parameterized, so this is not SQL injection.
- Audit rows do not store the client address (`internal/app/app.go`). Login denials are harder to trace once everything appears to come from the proxy.
- Postgres is configured with `sslmode=disable` (`internal/config/config.go`). The database network is internal and only the app joins it. Acceptable for this Compose layout; not acceptable if that network is ever shared.
- The CSRF value is copied into module state in an effect (`web/src/main.tsx`). A write in the same turn as the first authenticated paint can go out with an empty token and get a 403. Refresh fixes it.

## 5. Checked and sound

**Authentication.** Argon2id (`m=65536,t=3,p=2`) with a 16-byte salt, constant-time compare, a two-slot hash gate, and a 12–256 character password. Session cookies are `HttpOnly`. The absolute lifetime is 12 hours and the idle timeout is enforced in the `UPDATE ... WHERE touched_at` predicate. API tokens are stored as SHA-256 digests, scoped, bound to sites, and revocable. Bearer requests skip CSRF on purpose. Unknown token scopes are rejected.

**Random salt generation.** The targeted Go 1.27 runtime's `crypto/rand.Read` fills the buffer or terminates the process if its underlying entropy read fails; it never returns an error to `passwordHash`. Ignoring its returned error does not establish a zero-salt failure path. See the [Go 1.27.2 API contract](https://pkg.go.dev/crypto/rand#Read).

**CSRF and browsers.** Writes require `Origin == DROP_ADMIN_ORIGIN` and `X-CSRF-Token == SHA-256("csrf:"+session)`. The React client sends both (`web/src/api.ts`). There is no CORS allow-list, so a content page cannot read admin JSON. JSON bodies are not a simple form POST. The admin CSP is `script-src 'self'` with no `unsafe-eval` (`internal/app/app.go`). User HTML, filenames, warnings, and descriptions are rendered as React text. There is no `dangerouslySetInnerHTML` under `web/src`.

**Uploads.** HTML and zip only. Paths reject `..`, absolute paths, backslashes, controls, and a sensitive-name list. Symlinks and non-regular zip entries are rejected. Declared and actual sizes are both limited. The worker sees only the `uploads` subpath, wipes its child environment, and the app re-runs `Inspect` under `quarantine` and compares `ReportHash` before renaming into `releases` (`cmd/drop-worker/main.go`, `internal/app/jobs.go`).

**Preview.** The grant is a 256-bit token in the URL fragment, redeemed once, and exchanged only when `Origin` is that preview host. The cookie is host-only. Caddy sends every other preview request through `/_internal/preview-auth` before `file_server`. Pause and delete revoke grants in the same transaction as the intent, and auth also checks `desired_status`.

**Routing.** Slugs cannot start with `d-` or use `api`, `www`, `admin`, or `drop`, so they do not collide with preview hosts. Unknown hosts get a static 404 with no file root. The admin router 404s unless `Host` equals `DROP_ADMIN_ORIGIN`. Content and admin suffixes are rejected at startup when they overlap.

**Frontend upload progress.** Directory packing reports progress from the zip worker, and the multipart body uses `xhr.upload.onprogress` (`web/src/pages/UploadPage.tsx`, `web/src/api.ts`). The browser publish path re-reads `expectedGeneration` immediately before publish (`UploadPage.tsx`).

**Migrations and restore.** `schema_migrations` updates are transactional and serialized with an advisory lock (`db/migrations/migrate.go`). `scripts/restore.py` refuses an existing target directory and a non-empty database, checks artifact hashes, and does not point the live gateway at the restored tree.

**Compose defaults.** Admin and content ports bind to `127.0.0.1`. Postgres has no published port. The worker has no database credentials and no release mount. Root filesystems are read-only and `no-new-privileges` is set. The private control/probe listeners still require the trusted-network boundary in section 2.5.

**Capability drop after secret loading.** Compose adds `SETUID`, `SETGID`, and `DAC_OVERRIDE` for the root entrypoint. [su-exec calls setgid/setuid before exec](https://github.com/ncopa/su-exec/blob/master/su-exec.c); with the current startup path, Linux clears permitted, effective, and ambient capabilities when all user IDs become 10001. The bounding set can still list those capabilities and does not mean the long-running app or worker can use them. See [Linux capability transitions](https://man7.org/linux/man-pages/man7/capabilities.7.html). Process environment secrets remain a separate consequence of the entrypoint design.

## 6. Suggested order of work

1. Exercise reconciliation against real Caddy response loss, external configuration drift, missing snapshots, and unrelated queued changes; keep the global block until state is proved.
2. Validate the production proxy allowlist using the app-visible peer and real forwarded chains. Measure login availability under the retained global resource ceiling and external entry controls.
3. Protect Caddy control/probe reachability with an explicit trust boundary and test remote backup egress for any network change.
4. Test storage pressure with retained failed uploads, residual quarantine/release trees, long backups, and unavailable accounting; preserve reservation when measurement is uncertain.
5. Run the focused dotfile-entry test in section 3.5 before treating the rewrite risk as confirmed.
