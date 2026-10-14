package app

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/shixukai/self-hosted-drop/internal/archive"
	"golang.org/x/sys/unix"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (a *App) diskCheck(reserve int64) error {
	var s unix.Statfs_t
	if e := unix.Statfs(a.Config.DataDir, &s); e != nil {
		return e
	}
	free := int64(s.Bavail) * int64(s.Bsize)
	total := int64(s.Blocks) * int64(s.Bsize)
	// Filesystems with dynamic inode allocation (for example btrfs) report
	// Files=Ffree=0; that means no fixed inode budget, not an exhausted budget.
	if free < max(a.Config.DiskReserve, total/10)+reserve || (s.Files > 0 && s.Ffree < 20000) {
		return apiError(507, "STORAGE_LOW", "磁盘可用空间或 inode 不足")
	}
	return nil
}
func (a *App) upload(w http.ResponseWriter, r *http.Request) error {
	sid := chi.URLParam(r, "site")
	if e := a.require(r, "deployments:write", sid); e != nil {
		return e
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.Config.MaxUpload+(128<<10))
	reader, e := r.MultipartReader()
	if e != nil {
		return apiError(400, "INVALID_INPUT", "需要 multipart 上传")
	}
	part, e := reader.NextPart()
	if e != nil || part.FormName() != "metadata" {
		return apiError(400, "INVALID_INPUT", "metadata 必须位于文件之前")
	}
	raw, e := io.ReadAll(io.LimitReader(part, 65537))
	if e != nil || len(raw) > 65536 {
		return apiError(400, "INVALID_INPUT", "上传元数据过大")
	}
	var meta archive.Metadata
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&meta) != nil {
		return apiError(400, "INVALID_INPUT", "上传元数据无效")
	}
	if meta.InteractionMode == "" {
		meta.InteractionMode = "strict"
	}
	if meta.RoutingMode == "" {
		meta.RoutingMode = "static"
	}
	if meta.WarningsPolicy == "" {
		meta.WarningsPolicy = "require_ack"
	}
	if (meta.InteractionMode != "strict" && meta.InteractionMode != "interactive") || (meta.RoutingMode != "static" && meta.RoutingMode != "spa") || (meta.WarningsPolicy != "allow" && meta.WarningsPolicy != "require_ack") || len(meta.Message) > 1000 {
		return apiError(422, "INVALID_INPUT", "上传设置无效")
	}
	if meta.PublishAfterReady {
		if e = a.require(r, "publications:write", sid); e != nil {
			return e
		}
		if meta.ExpectedGeneration == nil {
			return apiError(422, "INVALID_INPUT", "自动发布需要 expectedGeneration")
		}
	}
	part, e = reader.NextPart()
	if e != nil || part.FormName() != "file" {
		return apiError(400, "INVALID_INPUT", "缺少文件")
	}
	name := part.FileName()
	if e = archive.ValidatePath(name, 1); e != nil {
		return apiError(422, "INVALID_INPUT", "文件名无效")
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".html" && ext != ".htm" && ext != ".zip" {
		return apiError(422, "INVALID_ARCHIVE", "仅支持 HTML、ZIP 或客户端打包后的目录")
	}
	reserve := a.Config.MaxUpload + 2*a.Config.MaxExpanded + (16 << 20)
	if e = a.diskCheck(reserve); e != nil {
		return e
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if _, e = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(82943003)"); e != nil {
		return e
	}
	var status string
	if e = tx.QueryRow(r.Context(), "SELECT status FROM sites WHERE id=$1 FOR UPDATE", sid).Scan(&status); e != nil {
		return e
	}
	if status == "DELETED" {
		return apiError(409, "SITE_DELETED", "请先恢复此站点")
	}
	var used int64
	var pending int
	if e = tx.QueryRow(r.Context(), "SELECT COALESCE((SELECT sum(storage_bytes) FROM deployments WHERE artifact_state IN('AVAILABLE','GC_PENDING')),0)+COALESCE((SELECT sum(reserved_bytes) FROM quota_reservations),0)").Scan(&used); e != nil {
		return e
	}
	if used+reserve > a.Config.Quota {
		return apiError(429, "QUOTA_EXCEEDED", "空间配额不足，无法预留本次上传")
	}
	if e = tx.QueryRow(r.Context(), "SELECT count(*) FROM deployments WHERE status IN('RECEIVING','QUEUED','VALIDATING','EXTRACTING','CHECKING')").Scan(&pending); e != nil {
		return e
	}
	if pending >= a.Config.QueueLimit {
		return apiError(429, "QUEUE_FULL", "处理队列已满")
	}
	did := id()
	if _, e = tx.Exec(r.Context(), "INSERT INTO deployments(id,site_id,status,filename,metadata,created_by) VALUES($1,$2,'RECEIVING',$3,$4,$5)", did, sid, name, jsonBytes(meta), actor(r).ID); e != nil {
		return e
	}
	if _, e = tx.Exec(r.Context(), "INSERT INTO quota_reservations(deployment_id,reserved_bytes) VALUES($1,$2)", did, reserve); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	dir := filepath.Join(a.Config.DataDir, "uploads", did)
	success := false
	defer func() {
		if !success {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			a.DB.Exec(ctx, "UPDATE deployments SET status='FAILED',error_code='UPLOAD_INTERRUPTED',error_message='上传未完整接收',updated_at=now() WHERE id=$1 AND status='RECEIVING'", did)
			os.RemoveAll(dir)
			a.DB.Exec(ctx, "DELETE FROM quota_reservations WHERE deployment_id=$1", did)
		}
	}()
	if e = os.Mkdir(dir, 0750); e != nil {
		return e
	}
	f, e := os.OpenFile(filepath.Join(dir, "payload"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if e != nil {
		return e
	}
	hash := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, hash), io.LimitReader(part, a.Config.MaxUpload+1))
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil || n > a.Config.MaxUpload || ((ext == ".html" || ext == ".htm") && n > a.Config.MaxFile) {
		return apiError(413, "UPLOAD_TOO_LARGE", "文件过大或传输中断")
	}
	if _, e = reader.NextPart(); e != io.EOF {
		return apiError(400, "INVALID_INPUT", "一次上传只允许一个文件部分")
	}
	if e = archive.SyncTree(dir); e != nil {
		return e
	}
	inputHash := hex.EncodeToString(hash.Sum(nil))
	fingerprint := digest(inputHash + name + string(jsonBytes(meta)))
	tx, e = a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if _, e = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(82943004)"); e != nil {
		return e
	}
	reused, e := idempotency(r, tx, "upload:"+sid, fingerprint, did)
	if e != nil {
		return e
	}
	if reused != "" {
		if e = tx.Commit(r.Context()); e != nil {
			return e
		}
		d, e := scanDeployment(a.DB.QueryRow(r.Context(), "SELECT "+deploymentFields+" FROM deployments WHERE id=$1", reused))
		if e != nil {
			return e
		}
		writeJSON(w, 202, d)
		return nil
	}
	if _, e = tx.Exec(r.Context(), "UPDATE deployments SET status='QUEUED',input_hash=$2,received_bytes=$3,updated_at=now() WHERE id=$1", did, inputHash, n); e != nil {
		return e
	}
	if _, e = tx.Exec(r.Context(), "INSERT INTO jobs(id,kind,resource_id) VALUES($1,'PROCESS',$2)", id(), did); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	success = true
	a.auditEvent(r.Context(), actor(r).ID, "UPLOAD", sid, did, "QUEUED")
	d, e := scanDeployment(a.DB.QueryRow(r.Context(), "SELECT "+deploymentFields+" FROM deployments WHERE id=$1", did))
	if e != nil {
		return e
	}
	writeJSON(w, 202, d)
	return nil
}
func idempotency(r *http.Request, tx pgx.Tx, operation, hash, resource string) (string, error) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return "", nil
	}
	if len(key) > 128 || strings.ContainsAny(key, "\r\n") {
		return "", apiError(400, "INVALID_IDEMPOTENCY_KEY", "幂等键无效")
	}
	var oldHash, oldID string
	e := tx.QueryRow(r.Context(), "SELECT request_hash,resource_id FROM idempotency_records WHERE actor_id=$1 AND operation=$2 AND key=$3", actor(r).ID, operation, key).Scan(&oldHash, &oldID)
	if e == nil {
		if oldHash != hash {
			return "", apiError(409, "IDEMPOTENCY_MISMATCH", "相同幂等键对应了不同请求")
		}
		return oldID, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return "", e
	}
	_, e = tx.Exec(r.Context(), "INSERT INTO idempotency_records(actor_id,operation,key,request_hash,resource_id) VALUES($1,$2,$3,$4,$5)", actor(r).ID, operation, key, hash, resource)
	return "", e
}
func (a *App) deployment(r *http.Request, scope string) (Deployment, error) {
	d, e := scanDeployment(a.DB.QueryRow(r.Context(), "SELECT "+deploymentFields+" FROM deployments WHERE id=$1", chi.URLParam(r, "deployment")))
	if e != nil {
		return d, e
	}
	return d, a.require(r, scope, d.SiteID)
}
func (a *App) getDeployment(w http.ResponseWriter, r *http.Request) error {
	d, e := a.deployment(r, "sites:read")
	if e != nil {
		return e
	}
	writeJSON(w, 200, d)
	return nil
}
func (a *App) listDeployments(w http.ResponseWriter, r *http.Request) error {
	sid := chi.URLParam(r, "site")
	if e := a.require(r, "sites:read", sid); e != nil {
		return e
	}
	offset, e := cursorOffset(r)
	if e != nil {
		return e
	}
	limit := pageLimit(r)
	rows, e := a.DB.Query(r.Context(), "SELECT "+deploymentFields+" FROM deployments WHERE site_id=$1 ORDER BY created_at DESC,id LIMIT $2 OFFSET $3", sid, limit+1, offset)
	if e != nil {
		return e
	}
	defer rows.Close()
	items := []Deployment{}
	for rows.Next() {
		d, e := scanDeployment(rows)
		if e != nil {
			return e
		}
		items = append(items, d)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	var cursor any
	if len(items) > limit {
		items = items[:limit]
		cursor = nextCursor(r, offset+limit)
	}
	writeJSON(w, 200, map[string]any{"items": items, "nextCursor": cursor})
	return nil
}
func (a *App) files(w http.ResponseWriter, r *http.Request) error {
	d, e := a.deployment(r, "sites:read")
	if e != nil {
		return e
	}
	offset, e := cursorOffset(r)
	if e != nil {
		return e
	}
	limit := pageLimit(r)
	if offset > len(d.Files) {
		offset = len(d.Files)
	}
	end := min(offset+limit, len(d.Files))
	var cursor any
	if end < len(d.Files) {
		cursor = nextCursor(r, end)
	}
	writeJSON(w, 200, map[string]any{"items": d.Files[offset:end], "nextCursor": cursor})
	return nil
}
func (a *App) selection(w http.ResponseWriter, r *http.Request) error {
	d, e := a.deployment(r, "deployments:write")
	if e != nil {
		return e
	}
	var req struct {
		RootDirectory      string `json:"rootDirectory"`
		EntryPath          string `json:"entryPath"`
		RoutingMode        string `json:"routingMode"`
		InspectionRevision int    `json:"inspectionRevision"`
	}
	if e = decode(w, r, &req); e != nil {
		return e
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if _, e = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(82943004)"); e != nil {
		return e
	}
	old, e := idempotency(r, tx, "selection:"+d.ID, digest(string(jsonBytes(req))), d.ID)
	if e != nil {
		return e
	}
	if old == "" {
		if req.InspectionRevision != d.Report.Revision {
			return apiError(409, "INSPECTION_CHANGED", "检查报告已更新，请刷新")
		}
		d.Metadata.RootDirectory = req.RootDirectory
		d.Metadata.EntryPath = req.EntryPath
		d.Metadata.RoutingMode = req.RoutingMode
		d.Metadata.InspectionRevision = req.InspectionRevision + 1
		tag, e := tx.Exec(r.Context(), "UPDATE deployments SET metadata=$2,status='QUEUED',updated_at=now() WHERE id=$1 AND status='AWAITING_INPUT'", d.ID, jsonBytes(d.Metadata))
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return apiError(409, "INVALID_STATE", "此部署不等待入口选择")
		}
		if _, e = tx.Exec(r.Context(), "INSERT INTO jobs(id,kind,resource_id) VALUES($1,'PROCESS',$2)", id(), d.ID); e != nil {
			return e
		}
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	writeJSON(w, 202, map[string]string{"id": d.ID, "status": "QUEUED"})
	return nil
}
func (a *App) cancelDeployment(w http.ResponseWriter, r *http.Request) error {
	d, e := a.deployment(r, "deployments:write")
	if e != nil {
		return e
	}
	tag, e := a.DB.Exec(r.Context(), "UPDATE deployments SET status='CANCELED',updated_at=now() WHERE id=$1 AND status IN('QUEUED','VALIDATING','EXTRACTING','CHECKING','AWAITING_INPUT')", d.ID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return apiError(409, "CANNOT_CANCEL", "此部署已完成或已进入发布阶段")
	}
	a.cancelMu.Lock()
	if c := a.cancels[d.ID]; c != nil {
		c()
	}
	a.cancelMu.Unlock()
	a.DB.Exec(r.Context(), "UPDATE jobs SET status='CANCELED' WHERE kind='PROCESS' AND resource_id=$1 AND status IN('PENDING','RUNNING')", d.ID)
	a.auditEvent(r.Context(), actor(r).ID, "DEPLOYMENT_CANCEL", d.SiteID, d.ID, "OK")
	writeJSON(w, 202, map[string]string{"id": d.ID, "status": "CANCELED"})
	return nil
}
func (a *App) retention(w http.ResponseWriter, r *http.Request) error {
	d, e := a.deployment(r, "sites:manage")
	if e != nil {
		return e
	}
	var req struct {
		Pinned bool `json:"pinned"`
	}
	if e = decode(w, r, &req); e != nil {
		return e
	}
	tag, e := a.DB.Exec(r.Context(), "UPDATE deployments SET pinned=$2 WHERE id=$1 AND artifact_state='AVAILABLE'", d.ID, req.Pinned)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return apiError(409, "ARTIFACT_UNAVAILABLE", "此版本文件不可用")
	}
	a.auditEvent(r.Context(), actor(r).ID, "RETENTION_CHANGE", d.SiteID, d.ID, "OK")
	d.Pinned = req.Pinned
	writeJSON(w, 200, d)
	return nil
}
func (a *App) exportDeployment(w http.ResponseWriter, r *http.Request) error {
	d, e := a.deployment(r, "sites:read")
	if e != nil {
		return e
	}
	if d.ArtifactState != "AVAILABLE" {
		return apiError(409, "ARTIFACT_UNAVAILABLE", "此版本文件不可用")
	}
	ctx := r.Context()
	pin := id()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var state string
	if e = tx.QueryRow(ctx, "SELECT artifact_state FROM deployments WHERE id=$1 FOR UPDATE", d.ID).Scan(&state); e != nil {
		return e
	}
	if state != "AVAILABLE" {
		return apiError(409, "ARTIFACT_UNAVAILABLE", "此版本文件不可用")
	}
	_, e = tx.Exec(ctx, "INSERT INTO artifact_pins(deployment_id,kind,reference_id,expires_at) VALUES($1,'EXPORT',$2,now()+interval '30 minutes')", d.ID, pin)
	if e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	defer a.DB.Exec(context.Background(), "DELETE FROM artifact_pins WHERE deployment_id=$1 AND reference_id=$2", d.ID, pin)
	root := filepath.Join(a.Config.DataDir, "releases", d.ID, "public")
	if e = archive.Verify(root, d.Report); e != nil {
		return apiError(409, "ARTIFACT_CORRUPT", "产物校验失败")
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="drop-`+d.ID+`.zip"`)
	z := zip.NewWriter(w)
	defer z.Close()
	for _, f := range d.Files {
		o, e := z.Create("content/" + f.Path)
		if e != nil {
			return nil
		}
		in, e := os.Open(filepath.Join(root, filepath.FromSlash(f.Path)))
		if e != nil {
			return nil
		}
		_, e = io.Copy(o, in)
		in.Close()
		if e != nil {
			return nil
		}
	}
	o, e := z.Create("drop-export.json")
	if e == nil {
		json.NewEncoder(o).Encode(map[string]any{"deploymentId": d.ID, "metadata": d.Metadata, "manifest": d.Report})
	}
	return nil
}
