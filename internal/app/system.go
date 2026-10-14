package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/shixukai/self-hosted-drop/internal/gateway"
	"golang.org/x/sys/unix"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (a *App) systemStatus(w http.ResponseWriter, r *http.Request) error {
	if e := a.require(r, "admin", ""); e != nil {
		return e
	}
	var used, reserved, sites, queue int64
	if e := a.DB.QueryRow(r.Context(), "SELECT COALESCE((SELECT sum(storage_bytes) FROM deployments WHERE artifact_state IN('AVAILABLE','GC_PENDING')),0),COALESCE((SELECT sum(reserved_bytes) FROM quota_reservations),0),(SELECT count(*) FROM sites WHERE status<>'DELETED'),(SELECT count(*) FROM jobs WHERE status IN('PENDING','RUNNING'))").Scan(&used, &reserved, &sites, &queue); e != nil {
		return e
	}
	var disk unix.Statfs_t
	unix.Statfs(a.Config.DataDir, &disk)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	_, _, gwErr := gateway.Read(ctx, a.Client, a.Config.CaddyAdmin)
	var backup *time.Time
	a.DB.QueryRow(r.Context(), "SELECT max(completed_at) FROM backup_runs WHERE status='COMPLETED'").Scan(&backup)
	writeJSON(w, 200, map[string]any{"version": "0.1.0", "usedBytes": used, "reservedBytes": reserved, "quotaBytes": a.Config.Quota, "siteCount": sites, "queuedJobs": queue, "diskFreeBytes": int64(disk.Bavail) * int64(disk.Bsize), "gatewayReady": gwErr == nil, "latestVerifiedBackup": backup, "backupConfigured": os.Getenv("RESTIC_REPOSITORY") != "", "adminOrigin": a.Config.AdminOrigin, "contentDomain": a.Config.ContentDomain})
	return nil
}
func (a *App) audit(w http.ResponseWriter, r *http.Request) error {
	if e := a.require(r, "admin", ""); e != nil {
		return e
	}
	offset, e := cursorOffset(r)
	if e != nil {
		return e
	}
	limit := pageLimit(r)
	rows, e := a.DB.Query(r.Context(), "SELECT id,actor,action,site_id,resource_id,request_id,result,created_at FROM audit_events ORDER BY id DESC LIMIT $1 OFFSET $2", limit+1, offset)
	if e != nil {
		return e
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var actor, action, request, result string
		var site, resource *string
		var at time.Time
		if e = rows.Scan(&id, &actor, &action, &site, &resource, &request, &result, &at); e != nil {
			return e
		}
		items = append(items, map[string]any{"id": id, "actor": actor, "action": action, "siteId": site, "resourceId": resource, "requestId": request, "result": result, "createdAt": at})
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
func (a *App) backups(w http.ResponseWriter, r *http.Request) error {
	if e := a.require(r, "admin", ""); e != nil {
		return e
	}
	rows, e := a.DB.Query(r.Context(), "SELECT id,status,snapshot_id,created_at,completed_at FROM backup_runs ORDER BY created_at DESC LIMIT 100")
	if e != nil {
		return e
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, status string
		var snapshot *string
		var created time.Time
		var completed *time.Time
		if e = rows.Scan(&id, &status, &snapshot, &created, &completed); e != nil {
			return e
		}
		items = append(items, map[string]any{"id": id, "status": status, "snapshotId": snapshot, "createdAt": created, "completedAt": completed})
	}
	writeJSON(w, 200, map[string]any{"items": items, "nextCursor": nil})
	return rows.Err()
}
func (a *App) settings(w http.ResponseWriter, r *http.Request) error {
	if e := a.require(r, "admin", ""); e != nil {
		return e
	}
	var revision int64
	var value []byte
	if e := a.DB.QueryRow(r.Context(), "SELECT revision,value FROM system_settings WHERE singleton").Scan(&revision, &value); e != nil {
		return e
	}
	writeJSON(w, 200, map[string]any{"revision": revision, "value": json.RawMessage(value), "runtimeLimits": map[string]any{"maxUploadBytes": a.Config.MaxUpload, "maxExpandedBytes": a.Config.MaxExpanded, "maxFiles": a.Config.MaxFiles, "retainVersions": a.Config.RetainVersions, "trashDays": a.Config.TrashDays}})
	return nil
}
func (a *App) updateSettings(w http.ResponseWriter, r *http.Request) error {
	if e := a.require(r, "admin", ""); e != nil {
		return e
	}
	var req struct {
		Revision int64 `json:"revision"`
		Value    struct {
			RetainVersions int `json:"retainVersions"`
			TrashDays      int `json:"trashDays"`
		} `json:"value"`
	}
	if e := decode(w, r, &req); e != nil {
		return e
	}
	if req.Value.RetainVersions < 1 || req.Value.RetainVersions > 100 || req.Value.TrashDays < 1 || req.Value.TrashDays > 365 {
		return apiError(422, "INVALID_INPUT", "保留版本须为 1–100，回收期须为 1–365 天")
	}
	var old []byte
	if e := a.DB.QueryRow(r.Context(), "SELECT value FROM system_settings WHERE singleton").Scan(&old); e != nil {
		return e
	}
	var previous struct {
		RetainVersions int `json:"retainVersions"`
		TrashDays      int `json:"trashDays"`
	}
	json.Unmarshal(old, &previous)
	if previous.RetainVersions == 0 {
		previous.RetainVersions = a.Config.RetainVersions
	}
	if previous.TrashDays == 0 {
		previous.TrashDays = a.Config.TrashDays
	}
	if req.Value.RetainVersions < previous.RetainVersions || req.Value.TrashDays < previous.TrashDays {
		return apiError(422, "RETENTION_REVIEW_REQUIRED", "缩短保留策略需先通过主机 gc --dry-run 核对；此接口只允许延长")
	}
	tag, e := a.DB.Exec(r.Context(), "UPDATE system_settings SET value=$1,revision=revision+1 WHERE singleton AND revision=$2", jsonBytes(req.Value), req.Revision)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return apiError(409, "SETTINGS_CONFLICT", "设置已变化，请刷新")
	}
	a.auditEvent(r.Context(), actor(r).ID, "SETTINGS_UPDATE", "", "", "OK")
	writeJSON(w, 200, map[string]any{"revision": req.Revision + 1, "value": req.Value})
	return nil
}
func (a *App) collect(ctx context.Context, dry bool, purgeSite string) error {
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
		return fmt.Errorf("gateway busy")
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(82943005)")
	if e = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(82943006)").Scan(&locked); e != nil {
		return e
	}
	if !locked {
		return fmt.Errorf("backup or GC in progress")
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(82943006)")
	actual, etag, e := gateway.Read(ctx, a.Client, a.Config.CaddyAdmin)
	if e != nil {
		return e
	}
	var pending bool
	if e = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM publications WHERE status IN('APPLYING','RECOVERING'))").Scan(&pending); e != nil {
		return e
	}
	if pending {
		return fmt.Errorf("gateway recovery in progress")
	}
	// Remove expired preview routes before considering a deployment for deletion.
	retain, trash := a.Config.RetainVersions, a.Config.TrashDays
	var settings []byte
	if e = conn.QueryRow(ctx, "SELECT value FROM system_settings WHERE singleton").Scan(&settings); e != nil {
		return e
	}
	var limits struct {
		RetainVersions int `json:"retainVersions"`
		TrashDays      int `json:"trashDays"`
	}
	json.Unmarshal(settings, &limits)
	if limits.RetainVersions > 0 {
		retain = limits.RetainVersions
	}
	if limits.TrashDays > 0 {
		trash = limits.TrashDays
	}
	const eligible = `WITH ranked AS (SELECT id,row_number() OVER(PARTITION BY site_id ORDER BY created_at DESC,id) AS rank FROM deployments WHERE artifact_state='AVAILABLE') SELECT d.id,d.expanded_bytes FROM deployments d JOIN sites s ON s.id=d.site_id LEFT JOIN ranked r ON r.id=d.id WHERE d.artifact_state IN('AVAILABLE','GC_PENDING') AND NOT d.pinned AND d.updated_at<now()-interval '120 seconds' AND (r.rank>$1 OR s.status='DELETED' AND (s.deleted_at<now()-make_interval(days=>$2) OR s.id=$3) OR d.artifact_state='GC_PENDING') AND (s.status='DELETED' OR d.id IS DISTINCT FROM s.applied_deployment_id AND d.id IS DISTINCT FROM s.desired_deployment_id) AND NOT EXISTS(SELECT 1 FROM publications p WHERE p.site_id=s.id AND p.status IN('PENDING','APPLYING','RECOVERING')) AND NOT EXISTS(SELECT 1 FROM preview_grants g WHERE g.deployment_id=d.id AND g.revoked_at IS NULL AND g.expires_at>now()) AND NOT EXISTS(SELECT 1 FROM artifact_pins p WHERE p.deployment_id=d.id AND (p.expires_at IS NULL OR p.expires_at>now())) ORDER BY d.id`
	rows, e := conn.Query(ctx, eligible, retain, trash, purgeSite)
	if e != nil {
		return e
	}
	type candidate struct {
		ID    string
		Bytes int64
	}
	items := []candidate{}
	for rows.Next() {
		var c candidate
		if e = rows.Scan(&c.ID, &c.Bytes); e != nil {
			rows.Close()
			return e
		}
		items = append(items, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if dry {
		return json.NewEncoder(os.Stdout).Encode(items)
	}
	if len(items) == 0 {
		return nil
	}
	tx, e := conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	kept := items[:0]
	for _, c := range items {
		var sid string
		if e = tx.QueryRow(ctx, "SELECT site_id FROM deployments WHERE id=$1", c.ID).Scan(&sid); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "SELECT 1 FROM sites WHERE id=$1 FOR UPDATE", sid); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "SELECT 1 FROM deployments WHERE id=$1 FOR UPDATE", c.ID); e != nil {
			return e
		}
		var ok bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM ("+eligible+") candidates WHERE id=$4)", retain, trash, purgeSite, c.ID).Scan(&ok); e != nil {
			return e
		}
		if !ok {
			continue
		}
		if _, e = tx.Exec(ctx, "UPDATE deployments SET artifact_state='GC_PENDING' WHERE id=$1", c.ID); e != nil {
			return e
		}
		kept = append(kept, c)
	}
	items = kept

	// Generating without GC_PENDING artifacts revokes both public preview and probe routes.
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	opts, e := a.gatewayOptions(ctx, nil)
	if e != nil {
		return e
	}
	cfg := gateway.Build(opts)
	if e = gateway.Apply(ctx, a.Client, a.Config.CaddyAdmin, etag, cfg); e != nil {
		return e
	}
	actual, _, e = gateway.Read(ctx, a.Client, a.Config.CaddyAdmin)
	if e != nil {
		return e
	}
	for _, c := range items {
		if strings.Contains(string(jsonBytes(actual)), "/"+c.ID+"/public") {
			return fmt.Errorf("artifact still routed")
		}
		if e = os.RemoveAll(filepath.Join(a.Config.DataDir, "releases", c.ID)); e != nil {
			return e
		}
		if _, e = conn.Exec(ctx, "UPDATE deployments SET artifact_state='PURGED' WHERE id=$1 AND artifact_state='GC_PENDING'", c.ID); e != nil {
			return e
		}
	}
	return nil
}
func (a *App) GC(ctx context.Context, dry bool) error { return a.collect(ctx, dry, "") }
