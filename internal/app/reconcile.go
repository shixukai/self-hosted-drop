package app

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shixukai/self-hosted-drop/internal/archive"
	"github.com/shixukai/self-hosted-drop/internal/gateway"
)

type ReconcileOptions struct {
	Mode               string
	ExpectedConfigHash string
}

type SiteDrift struct {
	SiteID              string  `json:"siteId"`
	Slug                string  `json:"slug"`
	AppliedStatus       string  `json:"appliedStatus"`
	DesiredStatus       string  `json:"desiredStatus"`
	AppliedGeneration   int64   `json:"appliedGeneration"`
	DesiredGeneration   int64   `json:"desiredGeneration"`
	AppliedDeploymentID *string `json:"appliedDeploymentId"`
	DesiredDeploymentID *string `json:"desiredDeploymentId"`
}

type ReconcileReport struct {
	PublicationID        string      `json:"publicationId"`
	Status               string      `json:"status"`
	Mode                 string      `json:"mode"`
	OperatorMode         string      `json:"operatorMode"`
	Action               string      `json:"action"`
	TargetStatus         string      `json:"targetStatus"`
	TargetGeneration     int64       `json:"targetGeneration"`
	DeploymentID         *string     `json:"deploymentId"`
	TargetConfigMatches  bool        `json:"targetConfigMatches"`
	AppliedConfigMatches bool        `json:"appliedConfigMatches"`
	GatewayConfigHash    string      `json:"gatewayConfigHash"`
	PendingPublications  []string    `json:"pendingPublications"`
	Drift                []SiteDrift `json:"drift"`
	VerifiedSites        int         `json:"verifiedSites"`
}

func sameDeployment(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (a *App) siteDrift(ctx context.Context) ([]SiteDrift, []string, error) {
	drift := []SiteDrift{}
	rows, e := a.DB.Query(ctx, "SELECT "+siteFields+" FROM sites ORDER BY id")
	if e != nil {
		return nil, nil, e
	}
	for rows.Next() {
		s, e := scanSite(rows)
		if e != nil {
			rows.Close()
			return nil, nil, e
		}
		// Failed intents consume generations. Only the desired/applied state and
		// active intents determine drift; an advanced counter alone is healthy.
		if s.Status != s.DesiredStatus || !sameDeployment(s.DeploymentID, s.DesiredDeploymentID) {
			drift = append(drift, SiteDrift{s.ID, s.Slug, s.Status, s.DesiredStatus, s.AppliedGeneration, s.Generation, s.DeploymentID, s.DesiredDeploymentID})
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, nil, e
	}
	pending := []string{}
	rows, e = a.DB.Query(ctx, "SELECT id FROM publications WHERE status IN('PENDING','APPLYING','RECOVERING') ORDER BY created_at,id")
	if e != nil {
		return nil, nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, nil, e
		}
		pending = append(pending, id)
	}
	return drift, pending, rows.Err()
}

func configHash(cfg gateway.M) string { return digest(string(jsonBytes(cfg))) }

// Reconcile is a host maintenance operation. Inspection is read-only; mutations
// require the exact observed config digest and never blindly abandon an intent.
func (a *App) Reconcile(ctx context.Context, pid string, options ReconcileOptions) (report ReconcileReport, err error) {
	mode := options.Mode
	if mode == "" {
		mode = "inspect"
	}
	report = ReconcileReport{PublicationID: pid, Mode: mode, PendingPublications: []string{}, Drift: []SiteDrift{}}
	if mode != "inspect" && mode != "complete" && mode != "restore-applied" {
		return report, apiError(http.StatusBadRequest, "INVALID_RECONCILE_MODE", "恢复模式须为 inspect、complete 或 restore-applied")
	}
	if mode != "inspect" {
		b, e := hex.DecodeString(options.ExpectedConfigHash)
		if e != nil || len(b) != 32 {
			return report, apiError(422, "CONFIG_HASH_REQUIRED", "请先检查状态，并提供当时的 gatewayConfigHash")
		}
	}
	conn, e := a.DB.Acquire(ctx)
	if e != nil {
		return report, e
	}
	defer conn.Release()
	if a.DB.Config().MaxConns < 2 {
		return report, fmt.Errorf("reconciliation requires a database pool with at least two connections")
	}
	// All acquisitions are nonblocking. GC uses a different lock order; waiting
	// while holding another lock here would create a coordinator/GC deadlock.
	for _, lock := range []int64{82943005, 82943004, 82943006} {
		var acquired bool
		if e = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lock).Scan(&acquired); e != nil {
			return report, e
		}
		if !acquired {
			return report, apiError(409, "GATEWAY_BUSY", "发布、回收或备份正在处理，请稍后检查")
		}
		defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lock)
	}
	p, e := scanPublication(conn.QueryRow(ctx, "SELECT "+publicationFields+" FROM publications WHERE id=$1", pid))
	if e != nil {
		return report, e
	}
	report.Status = p.Status
	report.Action, report.TargetStatus, report.TargetGeneration, report.DeploymentID = p.Action, p.TargetStatus, p.TargetGeneration, p.DeploymentID
	if e = conn.QueryRow(ctx, "SELECT operator_reconcile_mode FROM publications WHERE id=$1", pid).Scan(&report.OperatorMode); e != nil {
		return report, e
	}
	report.Drift, report.PendingPublications, e = a.siteDrift(ctx)
	if e != nil {
		return report, e
	}
	actual, tag, e := gateway.Read(ctx, a.Client, a.Config.CaddyAdmin)
	if e != nil {
		return report, fmt.Errorf("cannot inspect gateway state")
	}
	report.GatewayConfigHash = configHash(actual)
	if mode == "inspect" {
		appliedOpts, e := a.gatewayOptions(ctx, nil)
		if e != nil {
			return report, e
		}
		targetOpts, e := a.gatewayOptions(ctx, &p)
		if e != nil {
			return report, e
		}
		report.AppliedConfigMatches = report.GatewayConfigHash == configHash(gateway.Build(appliedOpts))
		report.TargetConfigMatches = report.GatewayConfigHash == configHash(gateway.Build(targetOpts))
		return report, nil
	}
	if p.Status != "APPLYING" && p.Status != "RECOVERING" {
		return report, apiError(409, "PUBLICATION_NOT_RECOVERING", "仅可对账 APPLYING 或 RECOVERING 的操作")
	}
	if report.GatewayConfigHash != strings.ToLower(options.ExpectedConfigHash) {
		return report, apiError(409, "GATEWAY_CONFIG_CHANGED", "网关配置已变化，请重新检查后再决定恢复操作")
	}
	if tag == "" {
		return report, apiError(409, "GATEWAY_ETAG_REQUIRED", "网关未提供条件更新标识，拒绝恢复写入")
	}
	var other, revert bool
	if e = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM publications WHERE id<>$1 AND status IN('APPLYING','RECOVERING')),revert_required FROM publications WHERE id=$1", pid).Scan(&other, &revert); e != nil {
		return report, e
	}
	if other {
		return report, apiError(409, "OTHER_PUBLICATION_RECOVERING", "存在其他未确认的切换，需先排查，不能覆盖其状态")
	}
	if mode == "complete" && revert {
		return report, apiError(409, "RESTORE_REQUIRED", "该操作已要求恢复旧路由，请核验后使用 restore-applied")
	}
	if report.OperatorMode == "restore-applied" && mode == "complete" {
		return report, apiError(409, "OPERATOR_MODE_CONFLICT", "已有恢复已确认路由的意图，请继续 restore-applied")
	}
	// Persist the operator decision before verification or writes. A failed
	// verification (or CLI crash) must not be undone by the five-second job retry.
	if _, e = conn.Exec(ctx, "UPDATE publications SET status='RECOVERING',operator_reconcile_mode=$2,error_code='OPERATOR_RECONCILIATION_REQUIRED',error_message='维护对账尚未确认，需要继续原恢复操作' WHERE id=$1", pid, mode); e != nil {
		return report, e
	}
	report.Status, report.OperatorMode = "RECOVERING", mode
	var target *Publication
	if mode == "complete" {
		target = &p
	}
	opts, e := a.gatewayOptions(ctx, target)
	if e != nil {
		return report, e
	}
	expected := gateway.Build(opts)
	if mode == "complete" && configHash(actual) != configHash(expected) {
		return report, apiError(409, "GATEWAY_CONFIG_MISMATCH", "实际网关配置并非完整目标配置，不能补记发布成功")
	}
	// Restore verifies local artifacts before replacing any route. Content is
	// checked after readback so it is measured against the proposed baseline.
	if e = a.verifySiteArtifacts(ctx, target); e != nil {
		return report, e
	}
	if mode == "restore-applied" {
		applyErr := gateway.Apply(ctx, a.Client, a.Config.CaddyAdmin, tag, expected)
		observed, _, readErr := gateway.Read(ctx, a.Client, a.Config.CaddyAdmin)
		if readErr != nil || configHash(observed) != configHash(expected) {
			a.recovering(ctx, pid, fmt.Errorf("restored baseline not verified"))
			report.Status = "RECOVERING"
			return report, apiError(409, "GATEWAY_UNCERTAIN", "恢复配置尚未确认，保持 RECOVERING")
		}
		// A lost apply response is resolved by exact readback, not by retrying
		// blindly. Do not include gateway response bodies containing config data.
		_ = applyErr
		actual = observed
		report.GatewayConfigHash = configHash(actual)
	}
	if report.VerifiedSites, e = a.verifySiteContent(ctx, target); e != nil {
		a.recovering(ctx, pid, e)
		report.Status = "RECOVERING"
		return report, e
	}
	final, _, e := gateway.Read(ctx, a.Client, a.Config.CaddyAdmin)
	if e != nil || configHash(final) != configHash(expected) {
		a.recovering(ctx, pid, fmt.Errorf("gateway changed during reconciliation"))
		report.Status = "RECOVERING"
		return report, apiError(409, "GATEWAY_CONFIG_CHANGED", "校验过程中网关发生变化，保持 RECOVERING")
	}
	if e = a.commitReconciliation(ctx, conn, p, final, mode); e != nil {
		return report, e
	}
	if mode == "complete" {
		report.Status = "APPLIED"
	} else {
		report.Status = "FAILED"
	}
	report.OperatorMode = ""
	report.GatewayConfigHash = configHash(final)
	report.TargetConfigMatches = mode == "complete"
	report.AppliedConfigMatches = true
	report.Drift, report.PendingPublications, e = a.siteDrift(ctx)
	return report, e
}

func (a *App) reconciliationSites(ctx context.Context, target *Publication) ([]Site, error) {
	rows, e := a.DB.Query(ctx, "SELECT "+siteFields+" FROM sites ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	sites := []Site{}
	for rows.Next() {
		s, e := scanSite(rows)
		if e != nil {
			return nil, e
		}
		if target != nil && s.ID == target.SiteID {
			if s.Generation != target.TargetGeneration || s.DesiredStatus != target.TargetStatus || !sameDeployment(s.DesiredDeploymentID, target.DeploymentID) {
				return nil, apiError(409, "PUBLICATION_STATE_CHANGED", "站点期望状态与恢复操作不一致")
			}
			s.Status, s.DeploymentID, s.AppliedGeneration = target.TargetStatus, target.DeploymentID, target.TargetGeneration
		}
		sites = append(sites, s)
	}
	return sites, rows.Err()
}

func (a *App) verifySiteArtifacts(ctx context.Context, target *Publication) error {
	sites, e := a.reconciliationSites(ctx, target)
	if e != nil {
		return e
	}
	for _, s := range sites {
		if s.Status != "ACTIVE" {
			continue
		}
		if s.DeploymentID == nil {
			return fmt.Errorf("active site %s lacks an artifact", s.Slug)
		}
		d, e := scanDeployment(a.DB.QueryRow(ctx, "SELECT "+deploymentFields+" FROM deployments WHERE id=$1 AND site_id=$2", *s.DeploymentID, s.ID))
		if e != nil {
			return e
		}
		if d.Status != "READY" || d.ArtifactState != "AVAILABLE" {
			return fmt.Errorf("site %s artifact is not available", s.Slug)
		}
		if e = archive.Verify(filepath.Join(a.Config.DataDir, "releases", d.ID, "public"), d.Report); e != nil {
			return fmt.Errorf("site %s artifact verification failed: %w", s.Slug, e)
		}
	}
	return nil
}

func (a *App) verifySiteContent(ctx context.Context, target *Publication) (int, error) {
	sites, e := a.reconciliationSites(ctx, target)
	if e != nil {
		return 0, e
	}
	for i, s := range sites {
		if e = a.probePublication(ctx, Publication{SiteID: s.ID, TargetStatus: s.Status, DeploymentID: s.DeploymentID}); e != nil {
			return i, fmt.Errorf("site %s content verification failed: %w", s.Slug, e)
		}
	}
	return len(sites), nil
}

func (a *App) commitReconciliation(ctx context.Context, conn *pgxpool.Conn, p Publication, actual gateway.M, mode string) error {
	tx, e := conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	s, e := scanSite(tx.QueryRow(ctx, "SELECT "+siteFields+" FROM sites WHERE id=$1 FOR UPDATE", p.SiteID))
	if e != nil {
		return e
	}
	if s.Generation != p.TargetGeneration || s.DesiredStatus != p.TargetStatus || !sameDeployment(s.DesiredDeploymentID, p.DeploymentID) {
		return apiError(409, "PUBLICATION_STATE_CHANGED", "站点期望状态与恢复操作不一致")
	}
	var status string
	if e = tx.QueryRow(ctx, "SELECT status FROM publications WHERE id=$1 FOR UPDATE", p.ID).Scan(&status); e != nil {
		return e
	}
	if status != "APPLYING" && status != "RECOVERING" {
		return apiError(409, "PUBLICATION_STATE_CHANGED", "恢复操作状态已变化")
	}
	if mode == "complete" {
		_, e = tx.Exec(ctx, "UPDATE sites SET status=$2,applied_generation=$3,applied_deployment_id=$4,deleted_at=CASE WHEN $2='DELETED' THEN now() ELSE NULL END,updated_at=now() WHERE id=$1", p.SiteID, p.TargetStatus, p.TargetGeneration, p.DeploymentID)
		if e == nil {
			_, e = tx.Exec(ctx, "UPDATE publications SET status='APPLIED',revert_required=false,operator_reconcile_mode='',error_code='',error_message='',applied_at=now() WHERE id=$1", p.ID)
		}
	} else {
		_, e = tx.Exec(ctx, "UPDATE sites SET desired_status=status,desired_deployment_id=applied_deployment_id,updated_at=now() WHERE id=$1", p.SiteID)
		if e == nil {
			_, e = tx.Exec(ctx, "UPDATE publications SET status='FAILED',revert_required=false,operator_reconcile_mode='',error_code='OPERATOR_RESTORED',error_message='维护操作已校验并恢复已确认的路由' WHERE id=$1", p.ID)
		}
	}
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE jobs SET status='DONE',lease_until=NULL,attempt_id=NULL WHERE kind='PUBLISH' AND resource_id=$1 AND status IN('PENDING','RUNNING')", p.ID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO gateway_snapshots(config,config_hash) VALUES($1,$2)", jsonBytes(actual), configHash(actual)); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO audit_events(actor,action,site_id,resource_id,result) VALUES('operator','PUBLICATION_RECONCILE',$1,$2,$3)", p.SiteID, p.ID, mode); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
