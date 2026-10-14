package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

type actorKey struct{}
type Actor struct {
	ID           string
	Admin        bool
	Scopes       []string
	TokenID      string
	SessionToken string
}

func actor(r *http.Request) Actor { v, _ := r.Context().Value(actorKey{}).(Actor); return v }
func (a *App) require(r *http.Request, scope, siteID string) error {
	u := actor(r)
	if u.Admin {
		return nil
	}
	if scope == "admin" {
		return apiError(403, "FORBIDDEN", "此操作仅管理员可执行")
	}
	has := false
	for _, s := range u.Scopes {
		if s == scope {
			has = true
		}
	}
	if !has {
		return apiError(403, "FORBIDDEN", "Token 未获授此操作权限")
	}
	if siteID != "" {
		var ok bool
		if e := a.DB.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM api_token_sites WHERE token_id=$1 AND site_id=$2)", u.TokenID, siteID).Scan(&ok); e != nil {
			return e
		}
		if !ok {
			return apiError(404, "NOT_FOUND", "未找到此资源")
		}
	}
	return nil
}
func (a *App) rate(key string, max int) bool {
	a.rateMu.Lock()
	defer a.rateMu.Unlock()
	now := time.Now()
	v := a.rates[key]
	if now.After(v.Until) {
		v = rateBucket{Until: now.Add(time.Minute)}
	}
	v.Count++
	a.rates[key] = v
	if len(a.rates) > 10000 {
		for k, v := range a.rates {
			if now.After(v.Until) {
				delete(a.rates, k)
			}
		}
	}
	return v.Count <= max
}
func (a *App) loginRate(r *http.Request) error {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !a.rate("login-ip:"+host, 10) || !a.rate("login-global", 30) {
		return apiError(429, "RATE_LIMITED", "尝试次数过多，请稍后重试")
	}
	return nil
}

// Bound concurrent Argon2 allocations; reject excess requests before hashing.
func (a *App) authSlot() (func(), error) {
	select {
	case a.authGate <- struct{}{}:
		return func() { <-a.authGate }, nil
	default:
		return nil, apiError(429, "RATE_LIMITED", "登录服务繁忙，请稍后重试")
	}
}
func passwordHash(p string) string {
	s := make([]byte, 16)
	rand.Read(s)
	h := argon2.IDKey([]byte(p), s, 3, 64*1024, 2, 32)
	return "$argon2id$v=19$m=65536,t=3,p=2$" + base64.RawStdEncoding.EncodeToString(s) + "$" + base64.RawStdEncoding.EncodeToString(h)
}
func passwordOK(p, h string) bool {
	parts := strings.Split(h, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[3] != "m=65536,t=3,p=2" {
		return false
	}
	salt, e := base64.RawStdEncoding.DecodeString(parts[4])
	if e != nil || len(salt) != 16 {
		return false
	}
	want, e := base64.RawStdEncoding.DecodeString(parts[5])
	if e != nil || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(p), salt, 3, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}
func passwordValid(p string) bool { return utf8.RuneCountInString(p) >= 12 && len(p) <= 256 }
func (a *App) cookieName(preview bool) string {
	if preview {
		if a.Config.Development {
			return "drop_preview_dev"
		}
		return "__Host-drop_preview"
	}
	if a.Config.Development {
		return "drop_session_dev"
	}
	return "__Host-drop_session"
}
func (a *App) cookie(w http.ResponseWriter, name, value string, expiry time.Time) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Expires: expiry, MaxAge: int(time.Until(expiry).Seconds()), Secure: !a.Config.Development, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}
func (a *App) origin(r *http.Request) error {
	if r.Header.Get("Origin") != a.Config.AdminOrigin {
		return apiError(403, "ORIGIN_REJECTED", "请求来源不受信任")
	}
	return nil
}
func (a *App) setupStatus(w http.ResponseWriter, r *http.Request) error {
	var ready bool
	e := a.DB.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM administrators)").Scan(&ready)
	if e != nil {
		return e
	}
	writeJSON(w, 200, map[string]bool{"initialized": ready})
	return nil
}
func (a *App) setup(w http.ResponseWriter, r *http.Request) error {
	release, slotErr := a.authSlot()
	if slotErr != nil {
		return slotErr
	}
	defer release()
	if e := a.origin(r); e != nil {
		return e
	}
	if e := a.loginRate(r); e != nil {
		return e
	}
	var req struct {
		Key      string `json:"key"`
		Password string `json:"password"`
	}
	if e := decode(w, r, &req); e != nil {
		return e
	}
	if len(a.Config.SetupKey) < 32 || subtle.ConstantTimeCompare([]byte(digest(req.Key)), []byte(digest(a.Config.SetupKey))) != 1 {
		return apiError(403, "SETUP_KEY_INVALID", "初始化凭据无效")
	}
	if !passwordValid(req.Password) {
		return apiError(422, "WEAK_PASSWORD", "密码须为 12–256 字符")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if _, e = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(82943002)"); e != nil {
		return e
	}
	var exists bool
	if e = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM administrators)").Scan(&exists); e != nil {
		return e
	}
	if exists {
		return apiError(409, "ALREADY_INITIALIZED", "管理员已初始化")
	}
	uid := id()
	if _, e = tx.Exec(r.Context(), "INSERT INTO administrators(id,password_hash) VALUES($1,$2)", uid, passwordHash(req.Password)); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	a.auditEvent(r.Context(), uid, "SETUP", "", "", "OK")
	return a.newSession(w, r, uid, 201)
}
func (a *App) login(w http.ResponseWriter, r *http.Request) error {
	release, slotErr := a.authSlot()
	if slotErr != nil {
		return slotErr
	}
	defer release()
	if e := a.origin(r); e != nil {
		return e
	}
	if e := a.loginRate(r); e != nil {
		return e
	}
	var req struct {
		Password string `json:"password"`
	}
	if e := decode(w, r, &req); e != nil {
		return e
	}
	if len(req.Password) > 256 {
		return apiError(401, "INVALID_CREDENTIALS", "登录凭据错误")
	}
	var uid, h string
	e := a.DB.QueryRow(r.Context(), "SELECT id,password_hash FROM administrators WHERE singleton").Scan(&uid, &h)
	if e != nil && e != pgx.ErrNoRows {
		return e
	}
	if e != nil || !passwordOK(req.Password, h) {
		a.auditEvent(r.Context(), "anonymous", "LOGIN", "", "", "DENIED")
		return apiError(401, "INVALID_CREDENTIALS", "登录凭据错误")
	}
	a.auditEvent(r.Context(), uid, "LOGIN", "", "", "OK")
	return a.newSession(w, r, uid, 200)
}
func (a *App) newSession(w http.ResponseWriter, r *http.Request, uid string, status int) error {
	t := token()
	expires := time.Now().Add(12 * time.Hour)
	_, e := a.DB.Exec(r.Context(), "INSERT INTO admin_sessions(digest,administrator_id,expires_at) VALUES($1,$2,$3)", digest(t), uid, expires)
	if e != nil {
		return e
	}
	a.cookie(w, a.cookieName(false), t, expires)
	writeJSON(w, status, map[string]any{"administrator": true, "csrfToken": digest("csrf:" + t), "expiresAt": expires})
	return nil
}
func (a *App) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var u Actor
		ctx := r.Context()
		if auth := r.Header.Get("Authorization"); auth != "" {
			if !strings.HasPrefix(auth, "Bearer ") {
				a.fail(w, r, apiError(401, "AUTH_REQUIRED", "需要有效的 Bearer Token"))
				return
			}
			t := strings.TrimPrefix(auth, "Bearer ")
			if len(t) > 256 {
				a.fail(w, r, apiError(401, "AUTH_REQUIRED", "凭据无效"))
				return
			}
			e := a.DB.QueryRow(ctx, "SELECT id,scopes FROM api_tokens WHERE digest=$1 AND revoked_at IS NULL AND expires_at>now()", digest(t)).Scan(&u.TokenID, &u.Scopes)
			if e != nil {
				if e == pgx.ErrNoRows {
					e = apiError(401, "AUTH_REQUIRED", "Token 无效、过期或已撤销")
				}
				a.fail(w, r, e)
				return
			}
			u.ID = "token:" + u.TokenID
			a.DB.Exec(ctx, "UPDATE api_tokens SET last_used_at=now() WHERE id=$1", u.TokenID)
		} else {
			c, e := r.Cookie(a.cookieName(false))
			if e != nil {
				a.fail(w, r, apiError(401, "AUTH_REQUIRED", "请先登录"))
				return
			}
			u.SessionToken = c.Value
			e = a.DB.QueryRow(ctx, "UPDATE admin_sessions SET touched_at=now() WHERE digest=$1 AND expires_at>now() AND touched_at>now()-interval '30 minutes' RETURNING administrator_id", digest(c.Value)).Scan(&u.ID)
			if e != nil {
				if e == pgx.ErrNoRows {
					e = apiError(401, "AUTH_REQUIRED", "会话已过期，请重新登录")
				}
				a.fail(w, r, e)
				return
			}
			u.Admin = true
			if r.Method != "GET" && r.Method != "HEAD" {
				if e = a.origin(r); e != nil {
					a.fail(w, r, e)
					return
				}
				if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(digest("csrf:"+c.Value))) != 1 {
					a.fail(w, r, apiError(403, "CSRF_REJECTED", "页面验证已失效，请刷新重试"))
					return
				}
			}
		}
		if !a.rate("api:"+u.ID, 600) {
			a.fail(w, r, apiError(429, "RATE_LIMITED", "请求过于频繁"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, actorKey{}, u)))
	})
}
func (a *App) session(w http.ResponseWriter, r *http.Request) error {
	u := actor(r)
	if e := a.require(r, "admin", ""); e != nil {
		return e
	}
	writeJSON(w, 200, map[string]any{"administrator": true, "csrfToken": digest("csrf:" + u.SessionToken)})
	return nil
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) error {
	if e := a.require(r, "admin", ""); e != nil {
		return e
	}
	_, e := a.DB.Exec(r.Context(), "DELETE FROM admin_sessions WHERE digest=$1", digest(actor(r).SessionToken))
	if e != nil {
		return e
	}
	a.cookie(w, a.cookieName(false), "", time.Now().Add(-time.Hour))
	w.WriteHeader(204)
	return nil
}
func (a *App) password(w http.ResponseWriter, r *http.Request) error {
	release, slotErr := a.authSlot()
	if slotErr != nil {
		return slotErr
	}
	defer release()
	if e := a.require(r, "admin", ""); e != nil {
		return e
	}
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		Password        string `json:"password"`
	}
	if e := decode(w, r, &req); e != nil {
		return e
	}
	if e := a.loginRate(r); e != nil {
		return e
	}
	var h string
	if e := a.DB.QueryRow(r.Context(), "SELECT password_hash FROM administrators WHERE singleton").Scan(&h); e != nil {
		return e
	}
	if !passwordOK(req.CurrentPassword, h) {
		return apiError(403, "INVALID_CREDENTIALS", "当前密码不正确")
	}
	if !passwordValid(req.Password) {
		return apiError(422, "WEAK_PASSWORD", "密码须为 12–256 字符")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if _, e = tx.Exec(r.Context(), "UPDATE administrators SET password_hash=$1", passwordHash(req.Password)); e != nil {
		return e
	}
	if _, e = tx.Exec(r.Context(), "DELETE FROM admin_sessions"); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	a.cookie(w, a.cookieName(false), "", time.Now().Add(-time.Hour))
	a.auditEvent(r.Context(), actor(r).ID, "PASSWORD_CHANGE", "", "", "OK")
	w.WriteHeader(204)
	return nil
}

var scopes = map[string]bool{"sites:read": true, "deployments:write": true, "publications:write": true, "previews:write": true, "sites:manage": true, "sites:delete": true}

func (a *App) createToken(w http.ResponseWriter, r *http.Request) error {
	if e := a.require(r, "admin", ""); e != nil {
		return e
	}
	var req struct {
		Name      string    `json:"name"`
		Scopes    []string  `json:"scopes"`
		SiteIDs   []string  `json:"siteIds"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if e := decode(w, r, &req); e != nil {
		return e
	}
	if len(req.Name) < 1 || len(req.Name) > 100 || len(req.Scopes) == 0 || len(req.SiteIDs) == 0 || !req.ExpiresAt.After(time.Now()) || req.ExpiresAt.After(time.Now().Add(366*24*time.Hour)) {
		return apiError(422, "INVALID_INPUT", "请填写名称、项目范围、权限和一年以内的到期时间")
	}
	for _, s := range req.Scopes {
		if !scopes[s] {
			return apiError(422, "INVALID_INPUT", "权限范围无效")
		}
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	tid := id()
	raw := "drop_" + token()
	if _, e = tx.Exec(r.Context(), "INSERT INTO api_tokens(id,name,digest,prefix,scopes,expires_at) VALUES($1,$2,$3,$4,$5,$6)", tid, req.Name, digest(raw), raw[:13], req.Scopes, req.ExpiresAt); e != nil {
		return e
	}
	for _, s := range req.SiteIDs {
		var ok bool
		if e = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM sites WHERE id=$1 AND status<>'DELETED')", s).Scan(&ok); e != nil {
			return e
		}
		if !ok {
			return apiError(422, "INVALID_INPUT", "所选站点不存在")
		}
		if _, e = tx.Exec(r.Context(), "INSERT INTO api_token_sites(token_id,site_id) VALUES($1,$2) ON CONFLICT DO NOTHING", tid, s); e != nil {
			return e
		}
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	a.auditEvent(r.Context(), actor(r).ID, "TOKEN_CREATE", "", tid, "OK")
	writeJSON(w, 201, map[string]any{"id": tid, "token": raw, "name": req.Name, "expiresAt": req.ExpiresAt})
	return nil
}
func (a *App) listTokens(w http.ResponseWriter, r *http.Request) error {
	if e := a.require(r, "admin", ""); e != nil {
		return e
	}
	rows, e := a.DB.Query(r.Context(), `SELECT t.id,t.name,t.prefix,t.scopes,t.expires_at,t.revoked_at,t.last_used_at,t.created_at,COALESCE((SELECT array_agg(site_id) FROM api_token_sites WHERE token_id=t.id),'{}') FROM api_tokens t ORDER BY created_at DESC`)
	if e != nil {
		return e
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, prefix string
		var scopes, sites []string
		var expires, created time.Time
		var revoked, used *time.Time
		if e = rows.Scan(&id, &name, &prefix, &scopes, &expires, &revoked, &used, &created, &sites); e != nil {
			return e
		}
		items = append(items, map[string]any{"id": id, "name": name, "prefix": prefix, "scopes": scopes, "siteIds": sites, "expiresAt": expires, "revokedAt": revoked, "lastUsedAt": used, "createdAt": created})
	}
	if e = rows.Err(); e != nil {
		return e
	}
	writeJSON(w, 200, map[string]any{"items": items, "nextCursor": nil})
	return nil
}
func (a *App) revokeToken(w http.ResponseWriter, r *http.Request) error {
	if e := a.require(r, "admin", ""); e != nil {
		return e
	}
	t := chi.URLParam(r, "token")
	tag, e := a.DB.Exec(r.Context(), "UPDATE api_tokens SET revoked_at=now() WHERE id=$1", t)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	a.auditEvent(r.Context(), actor(r).ID, "TOKEN_REVOKE", "", t, "OK")
	w.WriteHeader(204)
	return nil
}
func (a *App) ResetPassword(ctx context.Context, p string) error {
	if !passwordValid(p) {
		return fmt.Errorf("password must contain at least 12 characters")
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	t, e := tx.Exec(ctx, "UPDATE administrators SET password_hash=$1", passwordHash(p))
	if e != nil {
		return e
	}
	if t.RowsAffected() == 0 {
		return fmt.Errorf("not initialized")
	}
	if _, e = tx.Exec(ctx, "DELETE FROM admin_sessions"); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
