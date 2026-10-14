package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/shixukai/self-hosted-drop/internal/archive"
	"github.com/shixukai/self-hosted-drop/internal/gateway"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type publishRequest struct {
	DeploymentID        string `json:"deploymentId"`
	ExpectedGeneration  *int64 `json:"expectedGeneration"`
	Action              string `json:"action"`
	ReportHash          string `json:"inspectionReportHash"`
	AcknowledgeWarnings bool   `json:"acknowledgeWarnings"`
	ConfirmSlug         string `json:"confirmSlug"`
}

func (a *App) publish(w http.ResponseWriter, r *http.Request) error {
	sid := chi.URLParam(r, "site")
	if e := a.require(r, "publications:write", sid); e != nil {
		return e
	}
	var req publishRequest
	if e := decode(w, r, &req); e != nil {
		return e
	}
	if req.Action == "" {
		req.Action = "PUBLISH"
	}
	if req.Action != "PUBLISH" && req.Action != "ROLLBACK" {
		return apiError(422, "INVALID_INPUT", "发布操作无效")
	}
	p, e := a.acceptPublication(r, sid, req)
	if e != nil {
		return e
	}
	writeJSON(w, 202, p)
	return nil
}
func (a *App) siteOperation(op string) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		sid := chi.URLParam(r, "site")
		scope := "sites:manage"
		if op == "delete" || op == "restore" {
			scope = "sites:delete"
		}
		if op == "purge" {
			scope = "admin"
		}
		if e := a.require(r, scope, sid); e != nil {
			return e
		}
		var req publishRequest
		if e := decode(w, r, &req); e != nil {
			return e
		}
		if op == "purge" {
			s, e := scanSite(a.DB.QueryRow(r.Context(), "SELECT "+siteFields+" FROM sites WHERE id=$1", sid))
			if e != nil {
				return e
			}
			if s.Status != "DELETED" || s.Slug != req.ConfirmSlug {
				return apiError(409, "CONFIRMATION_REQUIRED", "永久删除需要回收站状态和正确的站点标识")
			}
			if e = a.collect(r.Context(), false, sid); e != nil {
				return e
			}
			a.auditEvent(r.Context(), actor(r).ID, "SITE_PURGE", sid, sid, "OK")
			writeJSON(w, 202, map[string]string{"status": "GC_REQUESTED"})
			return nil
		}
		req.Action = strings.ToUpper(op)
		p, e := a.acceptPublication(r, sid, req)
		if e != nil {
			return e
		}
		writeJSON(w, 202, p)
		return nil
	}
}
func (a *App) acceptPublication(r *http.Request, sid string, req publishRequest) (Publication, error) {
	var p Publication
	ctx := r.Context()
	if req.ExpectedGeneration == nil {
		return p, apiError(422, "INVALID_INPUT", "缺少 expectedGeneration")
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return p, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(82943004)"); e != nil {
		return p, e
	}
	pid := id()
	old, e := idempotency(r, tx, "publication:"+sid, digest(string(jsonBytes(req))), pid)
	if e != nil {
		return p, e
	}
	if old != "" {
		p, e = scanPublication(tx.QueryRow(ctx, "SELECT "+publicationFields+" FROM publications WHERE id=$1", old))
		if e != nil {
			return p, e
		}
		return p, tx.Commit(ctx)
	}
	s, e := scanSite(tx.QueryRow(ctx, "SELECT "+siteFields+" FROM sites WHERE id=$1 FOR UPDATE", sid))
	if e != nil {
		return p, e
	}
	if s.Generation != *req.ExpectedGeneration {
		return p, apiError(409, "DEPLOYMENT_CONFLICT", "站点已变化，请刷新后重新确认")
	}
	var active bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM publications WHERE site_id=$1 AND status IN('PENDING','APPLYING','RECOVERING'))", sid).Scan(&active); e != nil {
		return p, e
	}
	if active {
		return p, apiError(409, "DEPLOYMENT_CONFLICT", "已有发布切换正在处理")
	}
	target := "ACTIVE"
	did := s.DeploymentID
	switch req.Action {
	case "PUBLISH", "ROLLBACK":
		if s.Status == "DELETED" {
			return p, apiError(409, "SITE_DELETED", "请先恢复站点")
		}
		d, e := scanDeployment(tx.QueryRow(ctx, "SELECT "+deploymentFields+" FROM deployments WHERE id=$1 AND site_id=$2 FOR UPDATE", req.DeploymentID, sid))
		if e != nil {
			return p, e
		}
		if d.Status != "READY" || d.ArtifactState != "AVAILABLE" {
			return p, apiError(409, "ARTIFACT_UNAVAILABLE", "目标版本尚未就绪或已回收")
		}
		if req.ReportHash != d.Report.ReportHash {
			return p, apiError(409, "INSPECTION_CHANGED", "检查报告不匹配，请重新查看")
		}
		if len(d.Report.Warnings) > 0 && !req.AcknowledgeWarnings {
			return p, apiError(422, "WARNINGS_NOT_ACKNOWLEDGED", "请先确认检查警告")
		}
		did = &d.ID
	case "PAUSE":
		if s.Status == "DELETED" {
			return p, apiError(409, "SITE_DELETED", "站点已删除")
		}
		target = "PAUSED"
	case "DELETE":
		if req.ConfirmSlug != s.Slug {
			return p, apiError(422, "CONFIRMATION_REQUIRED", "请输入完整站点标识确认删除")
		}
		target = "DELETED"
	case "RESTORE":
		if s.Status != "DELETED" {
			return p, apiError(409, "INVALID_STATE", "站点不在回收站")
		}
		target = "PAUSED"
	case "RESUME":
		if s.Status != "PAUSED" || did == nil {
			return p, apiError(409, "SITE_NOT_PUBLISHED", "没有可恢复的正式版本")
		}
	default:
		return p, apiError(422, "INVALID_INPUT", "操作无效")
	}
	if target == "ACTIVE" && req.Action == "RESUME" {
		var available bool
		if e = tx.QueryRow(ctx, "SELECT artifact_state='AVAILABLE' FROM deployments WHERE id=$1 FOR UPDATE", did).Scan(&available); e != nil {
			return p, e
		}
		if !available {
			return p, apiError(409, "ARTIFACT_UNAVAILABLE", "最近正式版本已回收，请上传新版本")
		}
	}
	if _, e = tx.Exec(ctx, "UPDATE sites SET generation=generation+1,desired_status=$2,desired_deployment_id=$3,updated_at=now() WHERE id=$1", sid, target, did); e != nil {
		return p, e
	}
	p, e = scanPublication(tx.QueryRow(ctx, "INSERT INTO publications(id,site_id,deployment_id,action,expected_generation,target_generation,target_status,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING "+publicationFields, pid, sid, did, req.Action, s.Generation, s.Generation+1, target, actor(r).ID))
	if e != nil {
		return p, e
	}
	if target == "PAUSED" || target == "DELETED" {
		if _, e = tx.Exec(ctx, "UPDATE preview_grants SET revoked_at=now() WHERE deployment_id IN(SELECT id FROM deployments WHERE site_id=$1) AND revoked_at IS NULL", sid); e != nil {
			return p, e
		}
	}
	if _, e = tx.Exec(ctx, "INSERT INTO jobs(id,kind,resource_id) VALUES($1,'PUBLISH',$2)", id(), pid); e != nil {
		return p, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO audit_events(actor,action,site_id,resource_id,result) VALUES($1,$2,$3,$4,'PENDING')", actor(r).ID, req.Action, sid, pid); e != nil {
		return p, e
	}
	return p, tx.Commit(ctx)
}
func (a *App) getPublication(w http.ResponseWriter, r *http.Request) error {
	p, e := scanPublication(a.DB.QueryRow(r.Context(), "SELECT "+publicationFields+" FROM publications WHERE id=$1", chi.URLParam(r, "publication")))
	if e != nil {
		return e
	}
	if e = a.require(r, "sites:read", p.SiteID); e != nil {
		return e
	}
	writeJSON(w, 200, p)
	return nil
}
func (a *App) autoPublish(ctx context.Context, d Deployment, report archive.Result) error {
	u := Actor{ID: d.CreatedBy, Admin: !strings.HasPrefix(d.CreatedBy, "token:")}
	if !u.Admin {
		u.TokenID = strings.TrimPrefix(d.CreatedBy, "token:")
		if e := a.DB.QueryRow(ctx, "SELECT scopes FROM api_tokens WHERE id=$1 AND revoked_at IS NULL AND expires_at>now()", u.TokenID).Scan(&u.Scopes); e != nil {
			a.auditEvent(ctx, u.ID, "AUTO_PUBLISH", d.SiteID, d.ID, "TOKEN_REVOKED")
			return nil
		}
	}
	r, _ := http.NewRequestWithContext(context.WithValue(ctx, actorKey{}, u), "POST", "http://internal", nil)
	r.Header.Set("Idempotency-Key", "auto-"+d.ID)
	if e := a.require(r, "publications:write", d.SiteID); e != nil {
		return nil
	}
	_, e := a.acceptPublication(r, d.SiteID, publishRequest{DeploymentID: d.ID, ExpectedGeneration: d.Metadata.ExpectedGeneration, Action: "PUBLISH", ReportHash: report.ReportHash, AcknowledgeWarnings: d.Metadata.WarningsPolicy == "allow"})
	if e != nil {
		a.auditEvent(ctx, u.ID, "AUTO_PUBLISH", d.SiteID, d.ID, "NOT_APPLIED")
	}
	return nil
}
func (a *App) gatewayOptions(ctx context.Context, target *Publication) (gateway.Options, error) {
	internal := os.Getenv("DROP_CADDY_AUTH_ADDRESS")
	if internal == "" {
		internal = "host.docker.internal:18181"
	}
	opts := gateway.Options{InternalAddress: internal, InternalKey: a.Config.InternalKey, AdminListen: "0.0.0.0:2019"}
	rows, e := a.DB.Query(ctx, "SELECT "+siteFields+" FROM sites ORDER BY id")
	if e != nil {
		return opts, e
	}
	sites := []Site{}
	for rows.Next() {
		s, e := scanSite(rows)
		if e != nil {
			rows.Close()
			return opts, e
		}
		sites = append(sites, s)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return opts, e
	}
	for _, s := range sites {
		status := s.Status
		did := s.DeploymentID
		gen := s.AppliedGeneration
		if target != nil && target.SiteID == s.ID {
			status = target.TargetStatus
			did = target.DeploymentID
			gen = target.TargetGeneration
		}
		if status == "DRAFT" {
			continue
		}
		rt := gateway.Route{ID: fmt.Sprintf("site-%s-g%d", s.ID, gen), Host: s.Slug + "." + a.Config.ContentDomain}
		if status == "PAUSED" {
			rt.Status = 503
		} else if status == "DELETED" {
			rt.Status = 404
		} else if did != nil {
			d, e := scanDeployment(a.DB.QueryRow(ctx, "SELECT "+deploymentFields+" FROM deployments WHERE id=$1", *did))
			if e != nil {
				return opts, e
			}
			rt.Root = filepath.ToSlash(filepath.Join(a.Config.CaddyRoot, d.ID, "public"))
			rt.Entry = d.Report.EntryPath
			rt.Mode = d.Report.RoutingMode
		} else {
			return opts, fmt.Errorf("active site lacks artifact")
		}
		opts.Routes = append(opts.Routes, rt)
	}
	rows, e = a.DB.Query(ctx, "SELECT "+deploymentFields+" FROM deployments WHERE artifact_state='AVAILABLE' ORDER BY id")
	if e != nil {
		return opts, e
	}
	defer rows.Close()
	for rows.Next() {
		d, e := scanDeployment(rows)
		if e != nil {
			return opts, e
		}
		rt := gateway.Route{ID: "probe-" + d.ID, Host: "d-" + d.ID + "." + a.Config.ContentDomain, Root: filepath.ToSlash(filepath.Join(a.Config.CaddyRoot, d.ID, "public")), Entry: d.Report.EntryPath, Mode: d.Report.RoutingMode}
		opts.Probes = append(opts.Probes, rt)
		rt.ID = "preview-" + d.ID
		rt.Preview = true
		opts.Routes = append(opts.Routes, rt)
	}
	return opts, rows.Err()
}
func (a *App) applyPublication(ctx context.Context, pid string) error {
	conn, e := a.DB.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	var locked bool
	if e = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(82943005)").Scan(&locked); e != nil {
		return e
	}
	if !locked {
		return fmt.Errorf("gateway coordinator busy")
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(82943005)")
	p, e := scanPublication(conn.QueryRow(ctx, "SELECT "+publicationFields+" FROM publications WHERE id=$1", pid))
	if e != nil {
		return e
	}
	if p.Status == "APPLIED" || p.Status == "FAILED" || p.Status == "CONFLICT" || p.Status == "CANCELED" {
		return nil
	}
	var blocked, revert bool
	if e = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM publications WHERE id<>$1 AND status IN('APPLYING','RECOVERING'))", pid).Scan(&blocked); e != nil {
		return e
	}
	if blocked {
		return fmt.Errorf("another publication requires recovery")
	}
	if e = conn.QueryRow(ctx, "SELECT revert_required FROM publications WHERE id=$1", pid).Scan(&revert); e != nil {
		return e
	}
	if revert {
		return a.compensate(ctx, p)
	}
	if _, e = conn.Exec(ctx, "UPDATE publications SET status='APPLYING' WHERE id=$1", pid); e != nil {
		return e
	}
	old, etag, e := gateway.Read(ctx, a.Client, a.Config.CaddyAdmin)
	if e != nil {
		return a.recovering(ctx, pid, e)
	}
	routeID := fmt.Sprintf("site-%s-g%d", p.SiteID, p.TargetGeneration)
	if !gateway.HasRoute(old, routeID) {
		if _, e = conn.Exec(ctx, "UPDATE publications SET previous_config=COALESCE(previous_config,$2) WHERE id=$1", pid, jsonBytes(old)); e != nil {
			return e
		}
		if p.TargetStatus == "ACTIVE" && p.DeploymentID != nil {
			d, e := scanDeployment(conn.QueryRow(ctx, "SELECT "+deploymentFields+" FROM deployments WHERE id=$1", *p.DeploymentID))
			if e != nil {
				return e
			}
			if e = archive.Verify(filepath.Join(a.Config.DataDir, "releases", d.ID, "public"), d.Report); e != nil {
				return a.failPublication(ctx, pid, "ARTIFACT_CORRUPT", "目标版本内容校验失败")
			}
		}
		opts, e := a.gatewayOptions(ctx, &p)
		if e != nil {
			return e
		}
		if p.TargetStatus == "ACTIVE" && p.DeploymentID != nil {
			candidate := gateway.WithProbe(old, opts)
			applyErr := gateway.Apply(ctx, a.Client, a.Config.CaddyAdmin, etag, candidate)
			_, newTag, readErr := gateway.Read(ctx, a.Client, a.Config.CaddyAdmin)
			if readErr != nil {
				return a.recovering(ctx, pid, readErr)
			}
			if applyErr != nil {
				return a.failPublication(ctx, pid, "PROBE_CONFIG_FAILED", "候选版本路由未就绪，原公开版本保持可用")
			}
			if e = a.probeContent(ctx, p, a.Config.CaddyProbe, "d-"+*p.DeploymentID+"."+contentHost(a.Config.ContentDomain)); e != nil {
				if _, er := conn.Exec(ctx, "UPDATE publications SET revert_required=true WHERE id=$1", pid); er != nil {
					return er
				}
				return a.compensate(ctx, p)
			}
			etag = newTag
		}
		cfg := gateway.Build(opts)
		if e = conn.Conn().Ping(ctx); e != nil {
			return e
		}
		applyErr := gateway.Apply(ctx, a.Client, a.Config.CaddyAdmin, etag, cfg)
		actual, _, readErr := gateway.Read(ctx, a.Client, a.Config.CaddyAdmin)
		if readErr != nil {
			return a.recovering(ctx, pid, readErr)
		}
		if !gateway.HasRoute(actual, routeID) {
			if applyErr != nil {
				return a.failPublication(ctx, pid, "ROUTE_APPLY_FAILED", "网关拒绝新配置，原版本保持可用")
			}
			return a.recovering(ctx, pid, fmt.Errorf("route was not observed"))
		}
	}
	if e = a.probePublication(ctx, p); e != nil {
		a.Log.Warn("publication content verification failed", "publicationId", pid, "error", e.Error())
		if _, e = conn.Exec(ctx, "UPDATE publications SET revert_required=true WHERE id=$1", pid); e != nil {
			return e
		}
		return a.compensate(ctx, p)
	}
	tx, e := conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "UPDATE sites SET status=$2,applied_generation=$3,applied_deployment_id=$4,deleted_at=CASE WHEN $2='DELETED' THEN now() ELSE NULL END,updated_at=now() WHERE id=$1 AND generation=$3", p.SiteID, p.TargetStatus, p.TargetGeneration, p.DeploymentID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE publications SET status='APPLIED',error_code='',error_message='',applied_at=now() WHERE id=$1", pid); e != nil {
		return e
	}
	actual, _, e := gateway.Read(ctx, a.Client, a.Config.CaddyAdmin)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO gateway_snapshots(config,config_hash) VALUES($1,$2)", jsonBytes(actual), digest(string(jsonBytes(actual)))); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	a.auditEvent(ctx, "controller", "PUBLICATION_APPLIED", p.SiteID, pid, "APPLIED")
	return nil
}
func (a *App) compensate(ctx context.Context, p Publication) error {
	var previous []byte
	if e := a.DB.QueryRow(ctx, "SELECT previous_config FROM publications WHERE id=$1", p.ID).Scan(&previous); e != nil {
		return a.recovering(ctx, p.ID, e)
	}
	var cfg gateway.M
	if json.Unmarshal(previous, &cfg) != nil || cfg == nil {
		return a.recovering(ctx, p.ID, fmt.Errorf("missing recovery snapshot"))
	}
	_, tag, e := gateway.Read(ctx, a.Client, a.Config.CaddyAdmin)
	if e != nil {
		return a.recovering(ctx, p.ID, e)
	}
	if e = gateway.Apply(ctx, a.Client, a.Config.CaddyAdmin, tag, cfg); e != nil {
		return a.recovering(ctx, p.ID, e)
	}
	actual, _, e := gateway.Read(ctx, a.Client, a.Config.CaddyAdmin)
	if e != nil {
		return a.recovering(ctx, p.ID, e)
	}
	if string(jsonBytes(actual)) != string(jsonBytes(cfg)) {
		return a.recovering(ctx, p.ID, fmt.Errorf("recovery config differs"))
	}
	s, e := scanSite(a.DB.QueryRow(ctx, "SELECT "+siteFields+" FROM sites WHERE id=$1", p.SiteID))
	if e != nil {
		return a.recovering(ctx, p.ID, e)
	}
	previousState := Publication{SiteID: s.ID, TargetStatus: s.Status, DeploymentID: s.DeploymentID}
	if e = a.probePublication(ctx, previousState); e != nil {
		return a.recovering(ctx, p.ID, e)
	}
	return a.failPublication(ctx, p.ID, "CONTENT_VERIFICATION_FAILED", "切换后内容校验失败，已验证并恢复原路由")
}
func (a *App) probePublication(ctx context.Context, p Publication) error {
	var slug string
	if e := a.DB.QueryRow(ctx, "SELECT slug FROM sites WHERE id=$1", p.SiteID).Scan(&slug); e != nil {
		return e
	}
	return a.probeContent(ctx, p, a.Config.CaddyContent, slug+"."+contentHost(a.Config.ContentDomain))
}
func (a *App) probeContent(ctx context.Context, p Publication, address, host string) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", address+"/", nil)
	req.Host = host
	req.Header.Set("Accept-Encoding", "identity")
	resp, e := a.Client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if p.TargetStatus != "ACTIVE" {
		want := 404
		if p.TargetStatus == "PAUSED" {
			want = 503
		}
		if resp.StatusCode != want {
			return fmt.Errorf("unexpected disabled status %d", resp.StatusCode)
		}
		return nil
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("unexpected content status %d", resp.StatusCode)
	}
	d, e := scanDeployment(a.DB.QueryRow(ctx, "SELECT "+deploymentFields+" FROM deployments WHERE id=$1", *p.DeploymentID))
	if e != nil {
		return e
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, a.Config.MaxFile+1))
	if e != nil {
		return e
	}
	for _, f := range d.Files {
		if f.Path == d.Report.EntryPath && archive.Sum(b) == f.SHA256 && resp.Header.Get("ETag") == "\""+f.SHA256+"\"" {
			return nil
		}
	}
	return fmt.Errorf("published entry hash differs")
}
func (a *App) recovering(ctx context.Context, pid string, cause error) error {
	a.DB.Exec(ctx, "UPDATE publications SET status='RECOVERING',error_code='GATEWAY_UNCERTAIN',error_message='网关状态尚未确认，正在恢复' WHERE id=$1", pid)
	return cause
}
func (a *App) failPublication(ctx context.Context, pid, code, msg string) error {
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var sid string
	if e = tx.QueryRow(ctx, "UPDATE publications SET status='FAILED',error_code=$2,error_message=$3 WHERE id=$1 RETURNING site_id", pid, code, msg).Scan(&sid); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE sites SET desired_status=status,desired_deployment_id=applied_deployment_id WHERE id=$1", sid); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (a *App) syncGateway(ctx context.Context) error {
	conn, e := a.DB.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	if _, e = conn.Exec(ctx, "SELECT pg_advisory_lock(82943005)"); e != nil {
		return e
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(82943005)")
	var pending bool
	if e = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM publications WHERE status IN('APPLYING','RECOVERING'))").Scan(&pending); e != nil {
		return e
	}
	if pending {
		return apiError(409, "GATEWAY_BUSY", "网关正在恢复，请稍后重试")
	}
	opts, e := a.gatewayOptions(ctx, nil)
	if e != nil {
		return e
	}
	_, tag, e := gateway.Read(ctx, a.Client, a.Config.CaddyAdmin)
	if e != nil {
		return e
	}
	return gateway.Apply(ctx, a.Client, a.Config.CaddyAdmin, tag, gateway.Build(opts))
}
func contentHost(c string) string {
	u, e := url.Parse("http://" + c)
	if e != nil {
		return c
	}
	return u.Hostname()
}

func (a *App) listPublications(w http.ResponseWriter, r *http.Request) error {
	sid := chi.URLParam(r, "site")
	if e := a.require(r, "sites:read", sid); e != nil {
		return e
	}
	offset, e := cursorOffset(r)
	if e != nil {
		return e
	}
	limit := pageLimit(r)
	rows, e := a.DB.Query(r.Context(), "SELECT "+publicationFields+" FROM publications WHERE site_id=$1 AND ($2='' OR deployment_id=$2) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4", sid, r.URL.Query().Get("deployment"), limit+1, offset)
	if e != nil {
		return e
	}
	defer rows.Close()
	items := []Publication{}
	for rows.Next() {
		p, e := scanPublication(rows)
		if e != nil {
			return e
		}
		items = append(items, p)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	var next any
	if len(items) > limit {
		items = items[:limit]
		next = nextCursor(r, offset+limit)
	}
	writeJSON(w, 200, map[string]any{"items": items, "nextCursor": next})
	return nil
}
