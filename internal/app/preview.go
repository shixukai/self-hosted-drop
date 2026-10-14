package app

import (
	"crypto/subtle"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var previewID = regexp.MustCompile(`^d-([a-f0-9]{32})$`)

func (a *App) createPreview(w http.ResponseWriter, r *http.Request) error {
	d, e := a.deployment(r, "previews:write")
	if e != nil {
		return e
	}
	if d.Status != "READY" || d.ArtifactState != "AVAILABLE" {
		return apiError(409, "ARTIFACT_UNAVAILABLE", "此部署不能预览")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	var status, desired string
	if e = tx.QueryRow(r.Context(), "SELECT status,desired_status FROM sites WHERE id=$1 FOR UPDATE", d.SiteID).Scan(&status, &desired); e != nil {
		return e
	}
	if status == "PAUSED" || status == "DELETED" || desired == "PAUSED" || desired == "DELETED" {
		return apiError(409, "PREVIEW_DISABLED", "暂停或删除的站点不能预览")
	}
	var state string
	if e = tx.QueryRow(r.Context(), "SELECT artifact_state FROM deployments WHERE id=$1 FOR UPDATE", d.ID).Scan(&state); e != nil {
		return e
	}
	if state != "AVAILABLE" {
		return apiError(409, "ARTIFACT_UNAVAILABLE", "版本不可用")
	}
	raw := token()
	gid := id()
	expires := time.Now().Add(time.Hour)
	if _, e = tx.Exec(r.Context(), "INSERT INTO preview_grants(id,deployment_id,digest,expires_at) VALUES($1,$2,$3,$4)", gid, d.ID, digest(raw), expires); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	if e = a.syncGateway(r.Context()); e != nil {
		a.DB.Exec(r.Context(), "UPDATE preview_grants SET revoked_at=now() WHERE id=$1", gid)
		return apiError(503, "PREVIEW_NOT_READY", "预览路由尚未就绪，请稍后重试")
	}
	a.auditEvent(r.Context(), actor(r).ID, "PREVIEW_CREATE", d.SiteID, d.ID, "OK")
	writeJSON(w, 201, map[string]any{"id": gid, "url": a.Config.SiteURL("d-"+d.ID) + "/_drop/preview#" + raw, "expiresAt": expires})
	return nil
}
func (a *App) revokePreview(w http.ResponseWriter, r *http.Request) error {
	gid := chi.URLParam(r, "grant")
	var sid string
	if e := a.DB.QueryRow(r.Context(), "SELECT d.site_id FROM preview_grants g JOIN deployments d ON d.id=g.deployment_id WHERE g.id=$1", gid).Scan(&sid); e != nil {
		return e
	}
	if e := a.require(r, "previews:write", sid); e != nil {
		return e
	}
	if _, e := a.DB.Exec(r.Context(), "UPDATE preview_grants SET revoked_at=now() WHERE id=$1", gid); e != nil {
		return e
	}
	a.auditEvent(r.Context(), actor(r).ID, "PREVIEW_REVOKE", sid, gid, "OK")
	w.WriteHeader(204)
	return nil
}
func (a *App) InternalRouter() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Drop-Internal")), []byte(a.Config.InternalKey)) != 1 {
			http.NotFound(w, r)
			return
		}
		host := strings.Split(r.Header.Get("X-Drop-Host"), ":")[0]
		suffix := "." + contentHost(a.Config.ContentDomain)
		if !strings.HasSuffix(host, suffix) {
			http.NotFound(w, r)
			return
		}
		m := previewID.FindStringSubmatch(strings.TrimSuffix(host, suffix))
		if m == nil {
			http.NotFound(w, r)
			return
		}
		did := m[1]
		var err error
		switch {
		case r.Method == "GET" && r.URL.Path == "/_drop/preview":
			a.previewPage(w, r)
			return
		case r.Method == "POST" && r.URL.Path == "/_drop/exchange":
			err = a.exchangePreview(w, r, did)
		case r.Method == "GET" && r.URL.Path == "/_internal/preview-auth":
			err = a.previewAuth(w, r, did)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			a.fail(w, r, err)
		}
	})
}
func (a *App) previewPage(w http.ResponseWriter, r *http.Request) {
	nonce := token()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	body := `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Drop 受控预览</title><style>body{font:16px system-ui;background:#fafbfe;color:#1c2333;max-width:460px;margin:18vh auto;padding:24px}h1{font-size:28px}p{line-height:1.8;color:#626f86}</style><h1>正在准备预览</h1><p id="status">正在验证一次性授权…</p><script nonce="` + nonce + `">const key=location.hash.slice(1);history.replaceState(null,'',location.pathname);if(!key){document.querySelector('#status').textContent='此预览需要新的授权链接。'}else{fetch('/_drop/exchange',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({token:key})}).then(async r=>{if(!r.ok)throw Error('授权已过期、已使用或已撤销。');location.replace('/');}).catch(e=>{document.querySelector('h1').textContent='暂时无法预览';document.querySelector('#status').textContent=e.message;});}</script></html>`
	w.Write([]byte(body))
}
func (a *App) exchangePreview(w http.ResponseWriter, r *http.Request, did string) error {
	if r.Header.Get("Origin") != a.Config.SiteURL("d-"+did) {
		return apiError(403, "ORIGIN_REJECTED", "来源无效")
	}
	var req struct {
		Token string `json:"token"`
	}
	if e := decode(w, r, &req); e != nil {
		return e
	}
	if len(req.Token) != 64 {
		return apiError(401, "PREVIEW_EXPIRED", "预览授权无效")
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return apiError(503, "DEPENDENCY_UNAVAILABLE", "预览鉴权暂不可用")
	}
	defer tx.Rollback(ctx)
	var gid string
	var expiry time.Time
	e = tx.QueryRow(ctx, `UPDATE preview_grants g SET redeemed_at=now() FROM deployments d,sites s WHERE g.digest=$1 AND g.deployment_id=$2 AND g.redeemed_at IS NULL AND g.revoked_at IS NULL AND g.expires_at>now() AND d.id=g.deployment_id AND d.artifact_state='AVAILABLE' AND s.id=d.site_id AND s.status NOT IN('PAUSED','DELETED') AND s.desired_status NOT IN('PAUSED','DELETED') RETURNING g.id,g.expires_at`, digest(req.Token), did).Scan(&gid, &expiry)
	if e == pgx.ErrNoRows {
		return apiError(401, "PREVIEW_EXPIRED", "预览授权已使用、过期或被撤销")
	}
	if e != nil {
		return apiError(503, "DEPENDENCY_UNAVAILABLE", "预览鉴权暂不可用")
	}
	raw := token()
	if _, e = tx.Exec(ctx, "INSERT INTO preview_sessions(digest,grant_id,deployment_id,expires_at) VALUES($1,$2,$3,$4)", digest(raw), gid, did, expiry); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	a.cookie(w, a.cookieName(true), raw, expiry)
	writeJSON(w, 200, map[string]bool{"authorized": true})
	return nil
}
func (a *App) previewAuth(w http.ResponseWriter, r *http.Request, did string) error {
	c, e := r.Cookie(a.cookieName(true))
	if e != nil {
		return apiError(401, "AUTH_REQUIRED", "需要预览授权")
	}
	var ok bool
	e = a.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM preview_sessions ps JOIN preview_grants g ON g.id=ps.grant_id JOIN deployments d ON d.id=ps.deployment_id JOIN sites s ON s.id=d.site_id WHERE ps.digest=$1 AND ps.deployment_id=$2 AND ps.expires_at>now() AND g.expires_at>now() AND g.revoked_at IS NULL AND d.artifact_state='AVAILABLE' AND s.status NOT IN('PAUSED','DELETED') AND s.desired_status NOT IN('PAUSED','DELETED'))`, digest(c.Value), did).Scan(&ok)
	if e != nil {
		return apiError(503, "DEPENDENCY_UNAVAILABLE", "预览鉴权暂不可用")
	}
	if !ok {
		return apiError(403, "PREVIEW_EXPIRED", "预览授权无效或已过期")
	}
	w.WriteHeader(204)
	return nil
}
