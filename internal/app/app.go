package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shixukai/self-hosted-drop/db/migrations"
	"github.com/shixukai/self-hosted-drop/internal/config"
)

type App struct {
	Config   config.Config
	DB       *pgxpool.Pool
	Log      *slog.Logger
	Client   *http.Client
	rateMu   sync.Mutex
	rates    map[string]rateBucket
	authGate chan struct{}
	cancelMu sync.Mutex
	cancels  map[string]context.CancelFunc
}
type rateBucket struct {
	Count int
	Until time.Time
}

func New(ctx context.Context, c config.Config) (*App, error) {
	db, e := pgxpool.New(ctx, c.DatabaseURL)
	if e != nil {
		return nil, e
	}
	if e = migrations.Apply(ctx, db); e != nil {
		db.Close()
		return nil, e
	}
	return &App{Config: c, DB: db, Log: slog.New(slog.NewJSONHandler(os.Stderr, nil)), Client: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, rates: map[string]rateBucket{}, authGate: make(chan struct{}, 2), cancels: map[string]context.CancelFunc{}}, nil
}
func (a *App) Close() { a.DB.Close() }
func (a *App) fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *APIError
	if !errors.As(err, &e) {
		if errors.Is(err, pgx.ErrNoRows) {
			e = apiError(404, "NOT_FOUND", "未找到此资源")
		} else {
			a.Log.Error("request failed", "requestId", middleware.GetReqID(r.Context()), "error", err.Error())
			e = apiError(500, "INTERNAL_ERROR", "请求未完成，请稍后重试")
		}
	}
	e.RequestID = middleware.GetReqID(r.Context())
	if e.Status == 429 {
		w.Header().Set("Retry-After", "60")
	}
	writeJSON(w, e.Status, e)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return apiError(400, "INVALID_INPUT", "请求内容无效")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return apiError(400, "INVALID_INPUT", "只允许一个 JSON 对象")
	}
	return nil
}
func (a *App) handle(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			a.fail(w, r, err)
		}
	}
}
func (a *App) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { r.Header.Del("X-Request-ID"); next.ServeHTTP(w, r) })
	}, middleware.RequestID, middleware.Recoverer)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Request-ID", middleware.GetReqID(r.Context()))
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "same-origin")
			u, _ := url.Parse(a.Config.AdminOrigin)
			if !strings.EqualFold(r.Host, u.Host) {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/health/live", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })
	r.Get("/health/ready", a.handle(func(w http.ResponseWriter, r *http.Request) error {
		if e := a.DB.Ping(r.Context()); e != nil {
			return apiError(503, "DEPENDENCY_UNAVAILABLE", "服务尚未就绪")
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return nil
	}))
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/setup/status", a.handle(a.setupStatus))
		r.Post("/setup", a.handle(a.setup))
		r.Post("/auth/login", a.handle(a.login))
		r.Group(func(r chi.Router) {
			r.Use(a.authenticate)
			r.Get("/auth/session", a.handle(a.session))
			r.Post("/auth/logout", a.handle(a.logout))
			r.Post("/auth/password", a.handle(a.password))
			r.Get("/sites", a.handle(a.listSites))
			r.Post("/sites", a.handle(a.createSite))
			r.Get("/sites/{site}", a.handle(a.getSite))
			r.Patch("/sites/{site}", a.handle(a.editSite))
			r.Post("/sites/{site}/deployments", a.handle(a.upload))
			r.Get("/sites/{site}/deployments", a.handle(a.listDeployments))
			r.Get("/deployments/{deployment}", a.handle(a.getDeployment))
			r.Get("/deployments/{deployment}/files", a.handle(a.files))
			r.Post("/deployments/{deployment}/selection", a.handle(a.selection))
			r.Post("/deployments/{deployment}/cancel", a.handle(a.cancelDeployment))
			r.Patch("/deployments/{deployment}/retention", a.handle(a.retention))
			r.Get("/deployments/{deployment}/export", a.handle(a.exportDeployment))
			r.Get("/sites/{site}/publications", a.handle(a.listPublications))
			r.Post("/sites/{site}/publications", a.handle(a.publish))
			r.Get("/publications/{publication}", a.handle(a.getPublication))
			for _, op := range []string{"pause", "resume", "restore", "purge"} {
				r.Post("/sites/{site}/"+op, a.handle(a.siteOperation(op)))
			}
			r.Delete("/sites/{site}", a.handle(a.siteOperation("delete")))
			r.Get("/tokens", a.handle(a.listTokens))
			r.Post("/tokens", a.handle(a.createToken))
			r.Delete("/tokens/{token}", a.handle(a.revokeToken))
			r.Post("/deployments/{deployment}/preview-grants", a.handle(a.createPreview))
			r.Delete("/preview-grants/{grant}", a.handle(a.revokePreview))
			r.Get("/system/status", a.handle(a.systemStatus))
			r.Get("/system/audit", a.handle(a.audit))
			r.Get("/system/backups", a.handle(a.backups))
			r.Get("/system/settings", a.handle(a.settings))
			r.Patch("/system/settings", a.handle(a.updateSettings))
		})
	})
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		p := filepath.Join(a.Config.WebDir, filepath.Clean("/"+r.URL.Path))
		if st, e := os.Stat(p); e != nil || st.IsDir() {
			p = filepath.Join(a.Config.WebDir, "index.html")
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data:; connect-src 'self'; worker-src 'self' blob:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, p)
	})
	return r
}
func (a *App) auditEvent(ctx context.Context, actor, action, site, resource, result string) {
	_, err := a.DB.Exec(ctx, "INSERT INTO audit_events(actor,action,site_id,resource_id,request_id,result) VALUES($1,$2,$3,$4,$5,$6)", actor, action, ptr(site), ptr(resource), middleware.GetReqID(ctx), result)
	if err != nil {
		a.Log.Error("audit write failed", "action", action)
	}
}
