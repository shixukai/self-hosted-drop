package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/shixukai/self-hosted-drop/internal/archive"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func (a *App) Run(ctx context.Context) {
	for i := 0; i < a.Config.Concurrency; i++ {
		go a.jobLoop(ctx)
	}
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				a.maintenance(ctx)
			}
		}
	}()
}
func (a *App) jobLoop(ctx context.Context) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if e := a.runJob(ctx); e != nil && e != pgx.ErrNoRows {
				a.Log.Error("job iteration", "error", e.Error())
			}
		}
	}
}
func (a *App) runJob(ctx context.Context) error {
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var jid, kind, resource string
	var attempts int
	e = tx.QueryRow(ctx, `SELECT id,kind,resource_id,attempts FROM jobs WHERE (status='PENDING' AND next_run_at<=now()) OR (status='RUNNING' AND lease_until<now()) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&jid, &kind, &resource, &attempts)
	if e != nil {
		return e
	}
	attempt := id()
	if _, e = tx.Exec(ctx, "UPDATE jobs SET status='RUNNING',attempt_id=$2,lease_until=now()+interval '30 seconds',attempts=attempts+1 WHERE id=$1", jid, attempt); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	taskCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	a.cancelMu.Lock()
	a.cancels[resource] = cancel
	a.cancelMu.Unlock()
	defer func() { a.cancelMu.Lock(); delete(a.cancels, resource); a.cancelMu.Unlock() }()
	go func() {
		t := time.NewTicker(8 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-taskCtx.Done():
				return
			case <-t.C:
				tag, e := a.DB.Exec(taskCtx, "UPDATE jobs SET lease_until=now()+interval '30 seconds' WHERE id=$1 AND attempt_id=$2 AND status='RUNNING'", jid, attempt)
				if e != nil || tag.RowsAffected() != 1 {
					cancel()
					return
				}
			}
		}
	}()
	if kind == "PROCESS" {
		e = a.process(taskCtx, resource, attempt, jid)
	} else if kind == "PUBLISH" {
		e = a.applyPublication(taskCtx, resource)
	} else {
		e = fmt.Errorf("unknown job kind")
	}
	finishCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	if e != nil {
		a.Log.Error("job failed", "kind", kind, "resourceId", resource, "error", e.Error())
		if kind == "PUBLISH" || attempts < 2 {
			_, err := a.DB.Exec(finishCtx, "UPDATE jobs SET status='PENDING',next_run_at=now()+interval '5 seconds' WHERE id=$1 AND attempt_id=$2 AND status='RUNNING'", jid, attempt)
			return err
		}
		a.failDeployment(finishCtx, resource, "PROCESSING_FAILED", "处理任务失败，可重新上传")
	}
	_, e = a.DB.Exec(finishCtx, "UPDATE jobs SET status='DONE',lease_until=NULL WHERE id=$1 AND attempt_id=$2 AND status='RUNNING'", jid, attempt)
	return e
}
func (a *App) failDeployment(ctx context.Context, did, code, msg string) {
	a.DB.Exec(ctx, "UPDATE deployments SET status='FAILED',error_code=$2,error_message=$3,updated_at=now() WHERE id=$1 AND status NOT IN('READY','CANCELED')", did, code, msg)
	a.DB.Exec(ctx, "UPDATE quota_reservations SET reserved_bytes=GREATEST(reserved_bytes,(SELECT received_bytes FROM deployments WHERE id=$1)) WHERE deployment_id=$1", did)
}
func (a *App) process(ctx context.Context, did, attempt, jid string) error {
	d, e := scanDeployment(a.DB.QueryRow(ctx, "SELECT "+deploymentFields+" FROM deployments WHERE id=$1", did))
	if e != nil {
		return e
	}
	if d.Status == "READY" && d.Metadata.PublishAfterReady && (len(d.Report.Warnings) == 0 || d.Metadata.WarningsPolicy == "allow") {
		return a.autoPublish(ctx, d, d.Report)
	}
	if d.Status == "CANCELED" || d.Status == "FAILED" || d.Status == "READY" {
		return nil
	}
	if _, e = a.DB.Exec(ctx, "UPDATE deployments SET status='CHECKING',updated_at=now() WHERE id=$1 AND status NOT IN('CANCELED','READY')", did); e != nil {
		return e
	}
	data := map[string]any{"deploymentId": did, "attemptId": attempt, "filename": d.Filename, "metadata": d.Metadata, "limits": archive.Limits{MaxExpanded: a.Config.MaxExpanded, MaxFile: a.Config.MaxFile, MaxFiles: a.Config.MaxFiles, MaxDepth: a.Config.MaxDepth}}
	req, e := http.NewRequestWithContext(ctx, "POST", a.Config.WorkerURL+"/process", bytes.NewReader(jsonBytes(data)))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+a.Config.WorkerKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 125 * time.Second}
	resp, e := client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("worker status %d", resp.StatusCode)
	}
	var result struct {
		Result  archive.Result `json:"result"`
		Code    string         `json:"code"`
		Message string         `json:"message"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&result); e != nil {
		return e
	}
	attemptDir := filepath.Join(a.Config.DataDir, "uploads", did, "attempts", attempt)
	defer os.RemoveAll(attemptDir)
	if result.Code != "" {
		a.failDeployment(ctx, did, result.Code, result.Message)
		return nil
	}
	report := result.Result
	if report.Status == "AWAITING_INPUT" {
		if d.Metadata.InteractionMode != "interactive" {
			a.failDeployment(ctx, did, "ENTRY_SELECTION_REQUIRED", "发现多个 HTML 入口，请指定 rootDirectory 和 entryPath")
			a.DB.Exec(ctx, "UPDATE deployments SET report=$2 WHERE id=$1", did, jsonBytes(report))
			return nil
		}
		_, e = a.DB.Exec(ctx, "UPDATE deployments SET status='AWAITING_INPUT',report=$2,updated_at=now() WHERE id=$1 AND status='CHECKING'", did, jsonBytes(report))
		return e
	}
	if report.Status != "READY" {
		return fmt.Errorf("invalid worker result")
	}
	quarantine := filepath.Join(a.Config.DataDir, "quarantine", did+"-"+attempt)
	if e = os.Rename(filepath.Join(attemptDir, "tree"), quarantine); e != nil {
		return e
	}
	defer os.RemoveAll(quarantine)
	// The worker process has exited. Reinspect in a directory outside its mount.
	verified, e := archive.Inspect(ctx, quarantine, d.Metadata, max(d.Metadata.InspectionRevision, 1))
	if e != nil {
		return e
	}
	if verified.ReportHash != report.ReportHash {
		return fmt.Errorf("worker result mismatch")
	}
	report = verified
	release := filepath.Join(a.Config.DataDir, "releases", did)
	staging := filepath.Join(a.Config.DataDir, "quarantine", "release-"+did+"-"+attempt)
	if e = os.Mkdir(staging, 0750); e != nil {
		return e
	}
	defer os.RemoveAll(staging)
	public := filepath.Join(staging, "public")
	if e = os.Rename(filepath.Join(quarantine, filepath.FromSlash(report.RootDirectory)), public); e != nil {
		return e
	}
	if e = archive.Verify(public, report); e != nil {
		return e
	}
	for _, f := range report.Files {
		if e = os.WriteFile(filepath.Join(public, filepath.FromSlash(f.Path))+".drop-etag", []byte(strconv.Quote(f.SHA256)), 0640); e != nil {
			return e
		}
	}
	if e = os.WriteFile(filepath.Join(staging, "manifest.json"), jsonBytes(report), 0640); e != nil {
		return e
	}
	if e = archive.SyncTree(staging); e != nil {
		return e
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var current, jobAttempt string
	if e = tx.QueryRow(ctx, "SELECT status FROM deployments WHERE id=$1 FOR UPDATE", did).Scan(&current); e != nil {
		return e
	}
	if e = tx.QueryRow(ctx, "SELECT attempt_id FROM jobs WHERE id=$1 AND status='RUNNING' AND lease_until>now() FOR UPDATE", jid).Scan(&jobAttempt); e != nil {
		return e
	}
	if current == "CANCELED" || jobAttempt != attempt {
		return nil
	}
	if _, err := os.Stat(release); err == nil {
		b, e := os.ReadFile(filepath.Join(release, "manifest.json"))
		if e != nil {
			return e
		}
		var old archive.Result
		if json.Unmarshal(b, &old) != nil || old.ArtifactHash != report.ArtifactHash {
			return fmt.Errorf("conflicting durable artifact")
		}
		if e = archive.Verify(filepath.Join(release, "public"), old); e != nil {
			return e
		}
	} else {
		if e = os.Rename(staging, release); e != nil {
			return e
		}
		f, e := os.Open(filepath.Dir(release))
		if e != nil {
			return e
		}
		e = f.Sync()
		f.Close()
		if e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, "UPDATE deployments SET status='READY',artifact_state='AVAILABLE',artifact_hash=$2,report=$3,files=$4,expanded_bytes=$5,file_count=$6,storage_bytes=$7,updated_at=now() WHERE id=$1", did, report.ArtifactHash, jsonBytes(report), jsonBytes(report.Files), report.ExpandedBytes, len(report.Files), report.ExpandedBytes+int64(len(report.Files))*66+int64(len(jsonBytes(report)))); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "DELETE FROM quota_reservations WHERE deployment_id=$1", did); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	os.Remove(filepath.Join(a.Config.DataDir, "uploads", did, "payload"))
	a.auditEvent(ctx, d.CreatedBy, "DEPLOYMENT_READY", d.SiteID, did, "OK")
	if d.Metadata.PublishAfterReady && (len(report.Warnings) == 0 || d.Metadata.WarningsPolicy == "allow") {
		return a.autoPublish(ctx, d, report)
	}
	return nil
}
func (a *App) maintenance(ctx context.Context) {
	a.DB.Exec(ctx, "DELETE FROM admin_sessions WHERE expires_at<now() OR touched_at<now()-interval '30 minutes'")
	a.DB.Exec(ctx, "DELETE FROM preview_sessions WHERE expires_at<now()")
	a.DB.Exec(ctx, "DELETE FROM artifact_pins WHERE expires_at<now()")
	a.DB.Exec(ctx, "DELETE FROM idempotency_records WHERE created_at<now()-interval '7 days'")
	a.DB.Exec(ctx, "UPDATE deployments SET status='FAILED',error_code='UPLOAD_INTERRUPTED',error_message='上传连接中断',updated_at=now() WHERE status='RECEIVING' AND updated_at<now()-interval '30 minutes'")
	rows, e := a.DB.Query(ctx, "SELECT id FROM deployments WHERE status IN('FAILED','CANCELED') AND updated_at<now()-interval '24 hours'")
	if e == nil {
		ids := []string{}
		for rows.Next() {
			var v string
			rows.Scan(&v)
			ids = append(ids, v)
		}
		rows.Close()
		for _, v := range ids {
			if os.RemoveAll(filepath.Join(a.Config.DataDir, "uploads", v)) == nil {
				a.DB.Exec(ctx, "DELETE FROM quota_reservations WHERE deployment_id=$1", v)
			}
		}
	}
	if e = a.collect(ctx, false, ""); e != nil {
		a.Log.Warn("retention postponed", "reason", e.Error())
	}
}
