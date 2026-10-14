package app

import (
	"encoding/base64"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

func validSlug(s string) bool {
	return slugRE.MatchString(s) && !strings.HasPrefix(s, "d-") && s != "api" && s != "www" && s != "admin" && s != "drop"
}
func (a *App) createSite(w http.ResponseWriter, r *http.Request) error {
	if e := a.require(r, "admin", ""); e != nil {
		return e
	}
	var req struct {
		Slug        string `json:"slug"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if e := decode(w, r, &req); e != nil {
		return e
	}
	req.Name = strings.TrimSpace(req.Name)
	if !validSlug(req.Slug) || utf8.RuneCountInString(req.Name) < 1 || utf8.RuneCountInString(req.Name) > 100 || len(req.Description) > 2000 {
		return apiError(422, "INVALID_INPUT", "名称和站点标识无效；标识须为 3–40 位小写字母、数字或连字符")
	}
	s, e := scanSite(a.DB.QueryRow(r.Context(), "INSERT INTO sites(id,slug,name,description) VALUES($1,$2,$3,$4) RETURNING "+siteFields, id(), req.Slug, req.Name, req.Description))
	if e != nil {
		var pe *pgconn.PgError
		if errors.As(e, &pe) && pe.Code == "23505" {
			return apiError(409, "SLUG_TAKEN", "该站点标识已被使用或保留")
		}
		return e
	}
	s.URL = a.Config.SiteURL(s.Slug)
	a.auditEvent(r.Context(), actor(r).ID, "SITE_CREATE", s.ID, s.ID, "OK")
	writeJSON(w, 201, s)
	return nil
}
func pageLimit(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n < 1 {
		n = 20
	}
	return min(n, 100)
}
func cursorOffset(r *http.Request) (int, error) {
	s := r.URL.Query().Get("cursor")
	if s == "" {
		return 0, nil
	}
	b, e := base64.RawURLEncoding.DecodeString(s)
	if e != nil {
		return 0, apiError(400, "INVALID_CURSOR", "分页游标无效")
	}
	parts := strings.Split(string(b), ":")
	if len(parts) != 2 || parts[0] != digest(r.URL.Path + "|" + r.URL.Query().Get("q") + "|" + r.URL.Query().Get("status"))[:12] {
		return 0, apiError(400, "INVALID_CURSOR", "分页条件已变化")
	}
	n, e := strconv.Atoi(parts[1])
	if e != nil || n < 0 || n > 10000000 {
		return 0, apiError(400, "INVALID_CURSOR", "分页游标无效")
	}
	return n, nil
}
func nextCursor(r *http.Request, n int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(digest(r.URL.Path + "|" + r.URL.Query().Get("q") + "|" + r.URL.Query().Get("status"))[:12] + ":" + strconv.Itoa(n)))
}
func (a *App) listSites(w http.ResponseWriter, r *http.Request) error {
	if e := a.require(r, "sites:read", ""); e != nil {
		return e
	}
	offset, e := cursorOffset(r)
	if e != nil {
		return e
	}
	limit := pageLimit(r)
	status := r.URL.Query().Get("status")
	q := r.URL.Query().Get("q")
	rows, e := a.DB.Query(r.Context(), "SELECT "+siteFields+` FROM sites WHERE ($1='' AND status<>'DELETED' OR status=$1) AND (name ILIKE '%'||$2||'%' OR slug ILIKE '%'||$2||'%') AND ($3 OR id IN(SELECT site_id FROM api_token_sites WHERE token_id=$4)) ORDER BY created_at DESC,id LIMIT $5 OFFSET $6`, status, q, actor(r).Admin, actor(r).TokenID, limit+1, offset)
	if e != nil {
		return e
	}
	items := []Site{}
	for rows.Next() {
		s, e := scanSite(rows)
		if e != nil {
			rows.Close()
			return e
		}
		s.URL = a.Config.SiteURL(s.Slug)
		items = append(items, s)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	var cursor any
	if len(items) > limit {
		items = items[:limit]
		cursor = nextCursor(r, offset+limit)
	}
	for i := range items {
		if e = a.DB.QueryRow(r.Context(), "SELECT count(*),COALESCE(sum(expanded_bytes) FILTER(WHERE artifact_state='AVAILABLE'),0) FROM deployments WHERE site_id=$1", items[i].ID).Scan(&items[i].DeploymentCount, &items[i].Bytes); e != nil {
			return e
		}
	}
	writeJSON(w, 200, map[string]any{"items": items, "nextCursor": cursor})
	return nil
}
func (a *App) getSite(w http.ResponseWriter, r *http.Request) error {
	sid := chi.URLParam(r, "site")
	if e := a.require(r, "sites:read", sid); e != nil {
		return e
	}
	s, e := scanSite(a.DB.QueryRow(r.Context(), "SELECT "+siteFields+" FROM sites WHERE id=$1", sid))
	if e != nil {
		return e
	}
	s.URL = a.Config.SiteURL(s.Slug)
	writeJSON(w, 200, s)
	return nil
}
func (a *App) editSite(w http.ResponseWriter, r *http.Request) error {
	sid := chi.URLParam(r, "site")
	if e := a.require(r, "sites:manage", sid); e != nil {
		return e
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if e := decode(w, r, &req); e != nil {
		return e
	}
	req.Name = strings.TrimSpace(req.Name)
	if utf8.RuneCountInString(req.Name) < 1 || utf8.RuneCountInString(req.Name) > 100 || len(req.Description) > 2000 {
		return apiError(422, "INVALID_INPUT", "名称须为 1–100 字符")
	}
	s, e := scanSite(a.DB.QueryRow(r.Context(), "UPDATE sites SET name=$1,description=$2,updated_at=$3 WHERE id=$4 AND status<>'DELETED' RETURNING "+siteFields, req.Name, req.Description, time.Now(), sid))
	if e != nil {
		return e
	}
	s.URL = a.Config.SiteURL(s.Slug)
	a.auditEvent(r.Context(), actor(r).ID, "SITE_EDIT", sid, sid, "OK")
	writeJSON(w, 200, s)
	return nil
}
