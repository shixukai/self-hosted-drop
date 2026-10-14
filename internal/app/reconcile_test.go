package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shixukai/self-hosted-drop/db/migrations"
	"github.com/shixukai/self-hosted-drop/internal/archive"
	"github.com/shixukai/self-hosted-drop/internal/config"
	"github.com/shixukai/self-hosted-drop/internal/gateway"
)

// These tests deliberately never consult DROP_DATABASE_URL. Every fixture owns a
// fresh schema in an explicitly supplied test database and talks only to httptest.
func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	dsn := os.Getenv("DROP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set DROP_TEST_DATABASE_URL to run isolated PostgreSQL recovery tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	schema := "drop_recovery_test_" + id()
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close(context.Background())
		t.Fatalf("create isolated test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("remove test schema: %v", err)
		}
		admin.Close(cleanupCtx)
	})
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// No public fallback: a typo or missing table cannot reach the user's schema.
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	poolConfig.MaxConns = 8
	db, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatalf("migrate isolated test schema: %v", err)
	}
	server := newRecoveryGateway(t)
	dataDir := t.TempDir()
	a := &App{
		DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Client: &http.Client{Timeout: 2 * time.Second},
		Config: config.Config{
			DataDir: dataDir, CaddyRoot: filepath.Join(dataDir, "releases"),
			CaddyAdmin: server.server.URL, CaddyContent: server.server.URL + "/public", CaddyProbe: server.server.URL + "/probe",
			ContentDomain: "recovery.test", AdminOrigin: "https://admin.recovery.test",
			InternalKey: "test-only-internal-secret-do-not-print", MaxFile: 1 << 20,
			RetainVersions: 10, TrashDays: 7,
		},
		rates: map[string]rateBucket{}, authGate: make(chan struct{}, 2), cancels: map[string]context.CancelFunc{},
	}
	fixtureCtx, fixtureCancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(fixtureCancel)
	f := &recoveryFixture{t: t, ctx: fixtureCtx, a: a, gateway: server}
	for _, site := range []struct{ id, slug, status string }{{"site-alpha", "alpha", "ACTIVE"}, {"site-beta", "beta", "ACTIVE"}, {"site-paused", "paused", "PAUSED"}} {
		f.exec("INSERT INTO sites(id,slug,name,status,desired_status,generation,applied_generation) VALUES($1,$2,$2,$3,$3,1,1)", site.id, site.slug, site.status)
	}
	f.old = f.artifact("deployment-old", "site-alpha", "<html>old release</html>")
	f.target = f.artifact("deployment-new", "site-alpha", "<html>new release</html>")
	f.other = f.artifact("deployment-other", "site-beta", "<html>unrelated release</html>")
	f.exec("UPDATE sites SET applied_deployment_id=$2,desired_deployment_id=$2 WHERE id=$1", "site-alpha", f.old.ID)
	f.exec("UPDATE sites SET applied_deployment_id=$2,desired_deployment_id=$2 WHERE id=$1", "site-beta", f.other.ID)
	f.exec("UPDATE sites SET generation=2,desired_deployment_id=$2 WHERE id=$1", "site-alpha", f.target.ID)
	f.exec("INSERT INTO publications(id,site_id,deployment_id,action,expected_generation,target_generation,target_status,status,created_by) VALUES('publication-target','site-alpha',$1,'PUBLISH',1,2,'ACTIVE','RECOVERING','test')", f.target.ID)
	f.exec("INSERT INTO jobs(id,kind,resource_id,status) VALUES('job-target','PUBLISH','publication-target','PENDING')")
	f.publication = Publication{ID: "publication-target", SiteID: "site-alpha", DeploymentID: &f.target.ID, Action: "PUBLISH", ExpectedGeneration: 1, TargetGeneration: 2, TargetStatus: "ACTIVE", Status: "RECOVERING"}
	f.gateway.setConfig(f.config(nil))
	return f
}

type recoveryFixture struct {
	t           *testing.T
	ctx         context.Context
	a           *App
	gateway     *recoveryGateway
	old, target Deployment
	other       Deployment
	publication Publication
}

func (f *recoveryFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.a.DB.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *recoveryFixture) artifact(did, sid, body string) Deployment {
	f.t.Helper()
	root := filepath.Join(f.a.Config.DataDir, "releases", did, "public")
	if err := os.MkdirAll(root, 0750); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(body), 0640); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "style.css"), []byte("body { color: black; }"), 0640); err != nil {
		f.t.Fatal(err)
	}
	report, err := archive.Inspect(f.ctx, root, archive.Metadata{EntryPath: "index.html", RoutingMode: "static"}, 1)
	if err != nil || report.Status != "READY" {
		f.t.Fatalf("inspect fixture artifact: status=%s err=%v", report.Status, err)
	}
	for _, file := range report.Files {
		if err = os.WriteFile(filepath.Join(root, file.Path)+".drop-etag", []byte(`"`+file.SHA256+`"`), 0640); err != nil {
			f.t.Fatal(err)
		}
	}
	f.exec("INSERT INTO deployments(id,site_id,status,artifact_state,artifact_hash,filename,report,files,expanded_bytes,file_count,created_by) VALUES($1,$2,'READY','AVAILABLE',$3,'fixture.zip',$4,$5,$6,$7,'test')", did, sid, report.ArtifactHash, jsonBytes(report), jsonBytes(report.Files), report.ExpandedBytes, len(report.Files))
	return Deployment{ID: did, SiteID: sid, Status: "READY", ArtifactState: "AVAILABLE", ArtifactHash: report.ArtifactHash, Report: report, Files: report.Files}
}

func (f *recoveryFixture) config(target *Publication) gateway.M {
	f.t.Helper()
	opts, err := f.a.gatewayOptions(f.ctx, target)
	if err != nil {
		f.t.Fatal(err)
	}
	return gateway.Build(opts)
}

func (f *recoveryFixture) snapshot() string {
	f.t.Helper()
	var value string
	err := f.a.DB.QueryRow(f.ctx, `SELECT jsonb_build_object(
		'sites',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM sites s),
		'deployments',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM deployments d),
		'publications',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM publications p),
		'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM jobs j),
		'pins',(SELECT jsonb_agg(to_jsonb(p) ORDER BY deployment_id,kind,reference_id) FROM artifact_pins p),
		'snapshots',(SELECT count(*) FROM gateway_snapshots),
		'audits',(SELECT count(*) FROM audit_events))::text`).Scan(&value)
	if err != nil {
		f.t.Fatal(err)
	}
	return value
}

func (f *recoveryFixture) inspect() ReconcileReport {
	f.t.Helper()
	before, posts := f.snapshot(), f.gateway.postCount()
	report, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{})
	if err != nil {
		f.t.Fatalf("inspect recovery: %v", err)
	}
	if report.PublicationID != f.publication.ID || len(report.GatewayConfigHash) != 64 {
		f.t.Fatalf("inspect omitted publication/config fingerprint: %+v", report)
	}
	if f.snapshot() != before || f.gateway.postCount() != posts {
		f.t.Fatal("inspect modified durable state or gateway")
	}
	return report
}

func (f *recoveryFixture) assertClosed() {
	f.t.Helper()
	var publicationStatus, jobStatus string
	if err := f.a.DB.QueryRow(f.ctx, "SELECT status FROM publications WHERE id=$1", f.publication.ID).Scan(&publicationStatus); err != nil {
		f.t.Fatal(err)
	}
	if err := f.a.DB.QueryRow(f.ctx, "SELECT status FROM jobs WHERE resource_id=$1 AND kind='PUBLISH'", f.publication.ID).Scan(&jobStatus); err != nil {
		f.t.Fatal(err)
	}
	if publicationStatus != "RECOVERING" || jobStatus == "DONE" {
		f.t.Fatalf("uncertain recovery released durable guard: publication=%s job=%s", publicationStatus, jobStatus)
	}
	site, err := scanSite(f.a.DB.QueryRow(f.ctx, "SELECT "+siteFields+" FROM sites WHERE id='site-alpha'"))
	if err != nil {
		f.t.Fatal(err)
	}
	if site.Status != "ACTIVE" || site.AppliedGeneration != 1 || site.DeploymentID == nil || *site.DeploymentID != f.old.ID || site.DesiredDeploymentID == nil || *site.DesiredDeploymentID != f.target.ID {
		f.t.Fatalf("uncertain recovery changed applied/desired state: %+v", site)
	}
	before := f.gateway.postCount()
	if err = f.a.syncGateway(f.ctx); err == nil {
		f.t.Error("ordinary gateway sync accepted an unresolved publication")
	}
	if err = f.a.GC(f.ctx, true); err == nil {
		f.t.Error("GC accepted an unresolved publication")
	}
	if f.gateway.postCount() != before {
		f.t.Error("blocked normal operations changed gateway config")
	}
}

func TestReconcileInspectIsReadOnlyAndRedactsConfig(t *testing.T) {
	f := newRecoveryFixture(t)
	before := f.snapshot()
	report := f.inspect()
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), f.a.Config.InternalKey) || strings.Contains(string(encoded), "X-Drop-Internal") || strings.Contains(string(encoded), f.a.Config.CaddyRoot) {
		t.Fatal("inspect report exposed gateway config or credentials")
	}
	if len(report.PendingPublications) == 0 || len(report.Drift) == 0 {
		t.Fatalf("inspect missed unresolved publication/desired drift: %s", encoded)
	}
	if after := f.snapshot(); after != before || f.gateway.postCount() != 0 {
		t.Fatal("inspect modified database or gateway")
	}
	if _, err = f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: "inspect"}); err != nil {
		t.Fatal(err)
	}
	if f.snapshot() != before || f.gateway.postCount() != 0 {
		t.Fatal("explicit inspect modified database or gateway")
	}
}

func TestReconcileCompleteConfirmsWholeTarget(t *testing.T) {
	f := newRecoveryFixture(t)
	f.exec("INSERT INTO jobs(id,kind,resource_id,status) VALUES('job-unrelated','PROCESS',$1,'PENDING')", f.publication.ID)
	// The gateway has committed the desired config, but the original publisher
	// lost its response before recording APPLIED in PostgreSQL.
	f.gateway.setConfig(f.config(&f.publication))
	report := f.inspect()
	if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: "complete", ExpectedConfigHash: report.GatewayConfigHash}); err != nil {
		t.Fatalf("confirm committed target: %v", err)
	}
	var status, job string
	var appliedAt *time.Time
	if err := f.a.DB.QueryRow(f.ctx, "SELECT status,applied_at FROM publications WHERE id=$1", f.publication.ID).Scan(&status, &appliedAt); err != nil {
		t.Fatal(err)
	}
	if err := f.a.DB.QueryRow(f.ctx, "SELECT status FROM jobs WHERE id='job-target'").Scan(&job); err != nil {
		t.Fatal(err)
	}
	site, err := scanSite(f.a.DB.QueryRow(f.ctx, "SELECT "+siteFields+" FROM sites WHERE id='site-alpha'"))
	if err != nil {
		t.Fatal(err)
	}
	if status != "APPLIED" || appliedAt == nil || job != "DONE" || site.AppliedGeneration != 2 || site.DeploymentID == nil || *site.DeploymentID != f.target.ID || site.DesiredDeploymentID == nil || *site.DesiredDeploymentID != f.target.ID {
		t.Fatalf("target confirmation incomplete: publication=%s appliedAt=%v job=%s site=%+v", status, appliedAt, job, site)
	}
	if f.gateway.postCount() != 0 {
		t.Fatal("complete rewrote an already committed gateway config")
	}
	var unrelatedJob string
	if err := f.a.DB.QueryRow(f.ctx, "SELECT status FROM jobs WHERE id='job-unrelated'").Scan(&unrelatedJob); err != nil {
		t.Fatal(err)
	}
	if unrelatedJob != "PENDING" {
		t.Fatal("complete changed an unrelated job sharing the same resource id")
	}
	if err = f.a.Doctor(f.ctx); err != nil {
		t.Fatalf("completed target fails doctor: %v", err)
	}
}

func TestReconcileRejectsUnknownModeWithoutMutation(t *testing.T) {
	f := newRecoveryFixture(t)
	before := f.snapshot()
	if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: "guess"}); err == nil {
		t.Fatal("recovery accepted an unknown mode")
	}
	if f.snapshot() != before || f.gateway.postCount() != 0 {
		t.Fatal("invalid recovery mode changed database or gateway")
	}
}

func TestReconcileCompleteRejectsDriftAndCorruption(t *testing.T) {
	cases := []struct {
		name   string
		change func(*recoveryFixture)
	}{
		{"full-config-drift-with-target-route", func(f *recoveryFixture) {
			cfg := f.gateway.config()
			cfg["unexpected"] = "foreign config change"
			f.gateway.setConfig(cfg)
		}},
		{"target-body-drift", func(f *recoveryFixture) {
			f.gateway.override("alpha.recovery.test", recoveryResponse{body: []byte("wrong target body")})
		}},
		{"other-site-body-drift", func(f *recoveryFixture) {
			f.gateway.override("beta.recovery.test", recoveryResponse{body: []byte("wrong unrelated body")})
		}},
		{"other-site-etag-drift", func(f *recoveryFixture) { f.gateway.override("beta.recovery.test", recoveryResponse{etag: `"wrong"`}) }},
		{"other-site-unavailable", func(f *recoveryFixture) { f.gateway.override("beta.recovery.test", recoveryResponse{status: 503}) }},
		{"disabled-site-status-drift", func(f *recoveryFixture) { f.gateway.override("paused.recovery.test", recoveryResponse{status: 200}) }},
		{"target-artifact-tampered", func(f *recoveryFixture) {
			if err := os.WriteFile(filepath.Join(f.a.Config.DataDir, "releases", f.target.ID, "public", "index.html"), []byte("tampered target"), 0640); err != nil {
				f.t.Fatal(err)
			}
			// A healthy-looking HTTP response must not hide corrupt local files.
			f.gateway.override("alpha.recovery.test", recoveryResponse{body: []byte("<html>new release</html>")})
		}},
		{"other-artifact-tampered", func(f *recoveryFixture) {
			if err := os.WriteFile(filepath.Join(f.a.Config.DataDir, "releases", f.other.ID, "public", "index.html"), []byte("tampered unrelated release"), 0640); err != nil {
				f.t.Fatal(err)
			}
			f.gateway.override("beta.recovery.test", recoveryResponse{body: []byte("<html>unrelated release</html>")})
		}},
		{"non-entry-asset-tampered", func(f *recoveryFixture) {
			if err := os.WriteFile(filepath.Join(f.a.Config.DataDir, "releases", f.target.ID, "public", "style.css"), []byte("body { color: white; }"), 0640); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"other-non-entry-asset-missing", func(f *recoveryFixture) {
			if err := os.Remove(filepath.Join(f.a.Config.DataDir, "releases", f.other.ID, "public", "style.css")); err != nil {
				f.t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.gateway.setConfig(f.config(&f.publication))
			tc.change(f)
			report := f.inspect()
			if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: "complete", ExpectedConfigHash: report.GatewayConfigHash}); err == nil {
				t.Fatal("complete accepted unverifiable target")
			}
			if f.gateway.postCount() != 0 {
				t.Fatal("complete mutated gateway on verification failure")
			}
			f.assertClosed()
		})
	}
}

func TestReconcileCompleteConfirmsDisabledTargets(t *testing.T) {
	for _, status := range []string{"PAUSED", "DELETED"} {
		t.Run(status, func(t *testing.T) {
			f := newRecoveryFixture(t)
			action := "PAUSE"
			if status == "DELETED" {
				action = "DELETE"
			}
			f.publication.TargetStatus = status
			f.publication.Action = action
			f.publication.DeploymentID = &f.old.ID
			f.exec("UPDATE publications SET target_status=$2,action=$3,deployment_id=$4 WHERE id=$1", f.publication.ID, status, action, f.old.ID)
			f.exec("UPDATE sites SET desired_status=$1,desired_deployment_id=$2 WHERE id='site-alpha'", status, f.old.ID)
			f.gateway.setConfig(f.config(&f.publication))
			report := f.inspect()
			if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: "complete", ExpectedConfigHash: report.GatewayConfigHash}); err != nil {
				t.Fatalf("complete disabled target %s: %v", status, err)
			}
			site, err := scanSite(f.a.DB.QueryRow(f.ctx, "SELECT "+siteFields+" FROM sites WHERE id='site-alpha'"))
			if err != nil {
				t.Fatal(err)
			}
			if site.Status != status || site.AppliedGeneration != 2 || site.DesiredStatus != status {
				t.Fatalf("disabled target not recorded: %+v", site)
			}
			if status == "DELETED" && site.DeletedAt == nil {
				t.Fatal("completed deletion omitted deleted_at")
			}
			if err := f.a.Doctor(f.ctx); err != nil {
				t.Fatalf("verified disabled target fails doctor: %v", err)
			}
		})
	}
}

func TestReconcileMutationRequiresFreshInspectionHash(t *testing.T) {
	for _, mode := range []string{"complete", "restore-applied"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.gateway.setConfig(f.config(&f.publication))
			if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: mode}); err == nil {
				t.Fatal("mutation accepted missing expected config hash")
			}
			report := f.inspect()
			cfg := f.gateway.config()
			cfg["changed-after-inspection"] = true
			f.gateway.setConfig(cfg)
			if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: mode, ExpectedConfigHash: report.GatewayConfigHash}); err == nil {
				t.Fatal("mutation accepted stale inspection hash")
			}
			if f.gateway.postCount() != 0 {
				t.Fatal("stale inspection caused a config POST")
			}
			f.assertClosed()
		})
	}
}

func TestRecoveryWorkerCannotBypassRefusedReconciliation(t *testing.T) {
	for _, scenario := range []string{"complete-config-drift", "complete-other-site-unavailable", "failed-restore"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.gateway.setConfig(f.config(&f.publication))
			mode := "complete"
			if scenario == "complete-config-drift" {
				cfg := f.gateway.config()
				cfg["unexpected"] = "unmanaged config change"
				f.gateway.setConfig(cfg)
			} else {
				f.gateway.override("beta.recovery.test", recoveryResponse{status: 503})
			}
			if scenario == "failed-restore" {
				mode = "restore-applied"
			}
			report := f.inspect()
			if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: mode, ExpectedConfigHash: report.GatewayConfigHash}); err == nil {
				t.Fatal("fixture failed to create an unresolved recovery")
			}
			// The ordinary PUBLISH job retries every five seconds. An operator's
			// refused recovery must remain guarded when that retry arrives.
			before := f.gateway.postCount()
			if err := f.a.applyPublication(f.ctx, f.publication.ID); err == nil {
				t.Fatal("ordinary worker bypassed a refused operator reconciliation")
			}
			if f.gateway.postCount() != before {
				t.Fatal("ordinary worker rewrote unresolved gateway state")
			}
			f.assertClosed()
		})
	}
}

func TestOrdinaryAutomaticRecoveryRejectsOtherSiteOrFullConfigDrift(t *testing.T) {
	cases := []struct {
		name   string
		change func(*recoveryFixture)
	}{
		{"full-config-drift", func(f *recoveryFixture) {
			cfg := f.gateway.config()
			cfg["unexpected"] = "foreign config change"
			f.gateway.setConfig(cfg)
		}},
		{"other-site-body-drift", func(f *recoveryFixture) {
			f.gateway.override("beta.recovery.test", recoveryResponse{body: []byte("wrong unrelated body")})
		}},
		{"other-site-non-entry-asset-tampered", func(f *recoveryFixture) {
			if err := os.WriteFile(filepath.Join(f.a.Config.DataDir, "releases", f.other.ID, "public", "style.css"), []byte("corrupt unrelated asset"), 0640); err != nil {
				f.t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.gateway.setConfig(f.config(&f.publication))
			tc.change(f)
			// No operator inspection or repair has run. This is the ordinary
			// retry of an uncertain publication whose target route already exists.
			if err := f.a.applyPublication(f.ctx, f.publication.ID); err == nil {
				t.Fatal("automatic recovery accepted unverified gateway/site state")
			}
			if f.gateway.postCount() != 0 {
				t.Fatal("automatic recovery rewrote config while confirming uncertainty")
			}
			f.assertClosed()
		})
	}
}

func TestReconcileRestoreAppliedWithoutPreviousSnapshot(t *testing.T) {
	f := newRecoveryFixture(t)
	baseline := f.config(nil)
	f.gateway.setConfig(f.config(&f.publication))
	report := f.inspect()
	if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: "restore-applied", ExpectedConfigHash: report.GatewayConfigHash}); err != nil {
		t.Fatalf("reconstruct applied baseline without previous_config: %v", err)
	}
	var status, job string
	var previous []byte
	if err := f.a.DB.QueryRow(f.ctx, "SELECT status,previous_config FROM publications WHERE id=$1", f.publication.ID).Scan(&status, &previous); err != nil {
		t.Fatal(err)
	}
	if err := f.a.DB.QueryRow(f.ctx, "SELECT status FROM jobs WHERE id='job-target'").Scan(&job); err != nil {
		t.Fatal(err)
	}
	site, err := scanSite(f.a.DB.QueryRow(f.ctx, "SELECT "+siteFields+" FROM sites WHERE id='site-alpha'"))
	if err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || job != "DONE" || site.Status != "ACTIVE" || site.DesiredStatus != site.Status || site.AppliedGeneration != 1 || site.Generation != 2 || site.DeploymentID == nil || *site.DeploymentID != f.old.ID || site.DesiredDeploymentID == nil || *site.DesiredDeploymentID != f.old.ID {
		t.Fatalf("restore did not preserve/reset database state: publication=%s job=%s site=%+v", status, job, site)
	}
	if string(jsonBytes(f.gateway.config())) != string(jsonBytes(baseline)) {
		t.Fatal("restored config does not match the complete applied database baseline")
	}
	if f.gateway.postCount() != 1 || f.gateway.lastIfMatch() == "" {
		t.Fatal("restore did not use one conditional gateway config write")
	}
	if err = f.a.Doctor(f.ctx); err != nil {
		t.Fatalf("healthy restored baseline with advanced generation fails doctor: %v", err)
	}
}

func TestReconcileCompleteCanFallBackToVerifiedAppliedBaseline(t *testing.T) {
	f := newRecoveryFixture(t)
	f.gateway.setConfig(f.config(&f.publication))
	if err := os.Remove(filepath.Join(f.a.Config.DataDir, "releases", f.target.ID, "public", "style.css")); err != nil {
		t.Fatal(err)
	}
	report := f.inspect()
	if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: "complete", ExpectedConfigHash: report.GatewayConfigHash}); err == nil {
		t.Fatal("complete accepted a target with a missing manifest asset")
	}
	// Once the target cannot be confirmed, a new inspection and explicit
	// restore decision must still allow the intact applied baseline to recover.
	report = f.inspect()
	if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: "restore-applied", ExpectedConfigHash: report.GatewayConfigHash}); err != nil {
		t.Fatalf("failed confirmation trapped a recoverable applied baseline: %v", err)
	}
	var status, operatorMode, job string
	if err := f.a.DB.QueryRow(f.ctx, "SELECT p.status,p.operator_reconcile_mode,j.status FROM publications p JOIN jobs j ON j.resource_id=p.id WHERE p.id=$1", f.publication.ID).Scan(&status, &operatorMode, &job); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || operatorMode != "" || job != "DONE" {
		t.Fatalf("safe fallback incomplete: publication=%s operator=%s job=%s", status, operatorMode, job)
	}
}

func TestReconcileRestoreResolvesLostApplyResponse(t *testing.T) {
	f := newRecoveryFixture(t)
	f.gateway.setConfig(f.config(&f.publication))
	report := f.inspect()
	f.gateway.mu.Lock()
	f.gateway.loseApplyResponse = true
	f.gateway.mu.Unlock()
	if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: "restore-applied", ExpectedConfigHash: report.GatewayConfigHash}); err != nil {
		t.Fatalf("verified readback did not resolve lost apply response: %v", err)
	}
	var status, job string
	if err := f.a.DB.QueryRow(f.ctx, "SELECT p.status,j.status FROM publications p JOIN jobs j ON j.resource_id=p.id WHERE p.id=$1", f.publication.ID).Scan(&status, &job); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || job != "DONE" {
		t.Fatalf("lost apply response left verified restore unfinished: publication=%s job=%s", status, job)
	}
	if f.gateway.postCount() != 1 {
		t.Fatal("lost response caused a duplicate gateway config write")
	}
}

func TestReconcileCompleteRefusesRequestedCompensation(t *testing.T) {
	f := newRecoveryFixture(t)
	f.exec("UPDATE publications SET revert_required=true WHERE id=$1", f.publication.ID)
	f.gateway.setConfig(f.config(&f.publication))
	report := f.inspect()
	if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: "complete", ExpectedConfigHash: report.GatewayConfigHash}); err == nil {
		t.Fatal("complete ignored an explicitly requested compensation")
	}
	f.assertClosed()
}

func TestReconcileDetectsGatewayChangeDuringVerification(t *testing.T) {
	for _, mode := range []string{"complete", "restore-applied"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.gateway.setConfig(f.config(&f.publication))
			report := f.inspect()
			f.gateway.mu.Lock()
			f.gateway.configChangeAfterContent = true
			f.gateway.mu.Unlock()
			if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: mode, ExpectedConfigHash: report.GatewayConfigHash}); err == nil {
				t.Fatal("recovery committed after gateway changed during content verification")
			}
			f.assertClosed()
		})
	}
}

func TestReconcileSerializesWithCoordinatorAcceptanceAndGC(t *testing.T) {
	for _, lock := range []int64{82943005, 82943004, 82943006} {
		t.Run(fmt.Sprint(lock), func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.gateway.setConfig(f.config(&f.publication))
			report := f.inspect()
			conn, err := f.a.DB.Acquire(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			if _, err = conn.Exec(f.ctx, "SELECT pg_advisory_lock($1)", lock); err != nil {
				t.Fatal(err)
			}
			defer func() {
				cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				conn.Exec(cleanupCtx, "SELECT pg_advisory_unlock($1)", lock)
			}()
			before := f.snapshot()
			ctx, cancel := context.WithTimeout(f.ctx, 200*time.Millisecond)
			defer cancel()
			if _, err = f.a.Reconcile(ctx, f.publication.ID, ReconcileOptions{Mode: "complete", ExpectedConfigHash: report.GatewayConfigHash}); err == nil {
				t.Fatalf("recovery ignored advisory lock %d", lock)
			}
			if f.snapshot() != before || f.gateway.postCount() != 0 {
				t.Fatal("blocked recovery modified database or gateway")
			}
		})
	}
}

func TestReconcileRestoreAppliedFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		change func(*recoveryFixture)
		posts  int
	}{
		{"baseline-artifact-tampered", func(f *recoveryFixture) {
			if err := os.WriteFile(filepath.Join(f.a.Config.DataDir, "releases", f.old.ID, "public", "index.html"), []byte("corrupt applied artifact"), 0640); err != nil {
				f.t.Fatal(err)
			}
		}, 0},
		{"other-baseline-artifact-tampered", func(f *recoveryFixture) {
			if err := os.WriteFile(filepath.Join(f.a.Config.DataDir, "releases", f.other.ID, "public", "index.html"), []byte("corrupt unrelated artifact"), 0640); err != nil {
				f.t.Fatal(err)
			}
		}, 0},
		{"baseline-non-entry-asset-tampered", func(f *recoveryFixture) {
			if err := os.WriteFile(filepath.Join(f.a.Config.DataDir, "releases", f.old.ID, "public", "style.css"), []byte("corrupt non-entry asset"), 0640); err != nil {
				f.t.Fatal(err)
			}
		}, 0},
		{"config-write-rejected", func(f *recoveryFixture) { f.gateway.setApplyFailure(http.StatusPreconditionFailed) }, 1},
		{"readback-config-drift", func(f *recoveryFixture) { f.gateway.setApplyDrift() }, 1},
		{"readback-unavailable", func(f *recoveryFixture) { f.gateway.setReadFailureAfterApply() }, 1},
		{"other-site-body-drift", func(f *recoveryFixture) {
			f.gateway.override("beta.recovery.test", recoveryResponse{body: []byte("unrelated drift")})
		}, 1},
		{"target-baseline-etag-drift", func(f *recoveryFixture) { f.gateway.override("alpha.recovery.test", recoveryResponse{etag: `"wrong"`}) }, 1},
		{"disabled-baseline-status-drift", func(f *recoveryFixture) { f.gateway.override("paused.recovery.test", recoveryResponse{status: 404}) }, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.gateway.setConfig(f.config(&f.publication))
			report := f.inspect()
			tc.change(f)
			if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: "restore-applied", ExpectedConfigHash: report.GatewayConfigHash}); err == nil {
				t.Fatal("restore accepted unverified baseline")
			}
			if got := f.gateway.postCount(); got != tc.posts {
				t.Fatalf("gateway writes=%d, want %d", got, tc.posts)
			}
			f.assertClosed()
		})
	}
}

func TestReconcileRejectsOtherUnresolvedPublication(t *testing.T) {
	for _, mode := range []string{"complete", "restore-applied"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.gateway.setConfig(f.config(&f.publication))
			f.exec("INSERT INTO publications(id,site_id,deployment_id,action,expected_generation,target_generation,target_status,status,created_by) VALUES('publication-other','site-beta',$1,'PAUSE',1,2,'PAUSED','APPLYING','test')", f.other.ID)
			report := f.inspect()
			if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: mode, ExpectedConfigHash: report.GatewayConfigHash}); err == nil {
				t.Fatal("recovery accepted another unresolved gateway transition")
			}
			if f.gateway.postCount() != 0 {
				t.Fatal("competing recovery changed gateway")
			}
			f.assertClosed()
		})
	}
}

func TestReconcileRejectsNonRecoveryStates(t *testing.T) {
	for _, status := range []string{"PENDING", "APPLIED", "FAILED", "CONFLICT", "CANCELED"} {
		t.Run(status, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.exec("UPDATE publications SET status=$2 WHERE id=$1", f.publication.ID, status)
			f.gateway.setConfig(f.config(&f.publication))
			report := f.inspect()
			before := f.snapshot()
			for _, mode := range []string{"complete", "restore-applied"} {
				if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: mode, ExpectedConfigHash: report.GatewayConfigHash}); err == nil {
					t.Fatalf("%s mutated non-recovery status %s", mode, status)
				}
			}
			if f.snapshot() != before || f.gateway.postCount() != 0 {
				t.Fatal("rejected recovery changed state")
			}
		})
	}
}

func TestReconcileAllowsApplyingState(t *testing.T) {
	for _, mode := range []string{"complete", "restore-applied"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.exec("UPDATE publications SET status='APPLYING' WHERE id=$1", f.publication.ID)
			f.gateway.setConfig(f.config(&f.publication))
			report := f.inspect()
			if _, err := f.a.Reconcile(f.ctx, f.publication.ID, ReconcileOptions{Mode: mode, ExpectedConfigHash: report.GatewayConfigHash}); err != nil {
				t.Fatalf("%s refused uncertain APPLYING publication: %v", mode, err)
			}
		})
	}
}

func TestDoctorRejectsPendingAndDesiredDrift(t *testing.T) {
	for _, change := range []string{"PENDING", "APPLYING", "RECOVERING", "desired-status", "desired-deployment"} {
		t.Run(change, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.exec("UPDATE sites SET desired_deployment_id=applied_deployment_id WHERE id='site-alpha'")
			f.exec("UPDATE publications SET status='FAILED' WHERE id=$1", f.publication.ID)
			switch change {
			case "PENDING", "APPLYING", "RECOVERING":
				f.exec("UPDATE publications SET status=$2 WHERE id=$1", f.publication.ID, change)
			case "desired-status":
				f.exec("UPDATE sites SET desired_status='PAUSED' WHERE id='site-alpha'")
			case "desired-deployment":
				f.exec("UPDATE sites SET desired_deployment_id=$1 WHERE id='site-alpha'", f.target.ID)
			}
			if err := f.a.Doctor(f.ctx); err == nil {
				t.Fatalf("doctor reported healthy despite %s", change)
			}
		})
	}
}

func TestDoctorAllowsAdvancedGenerationAfterFailedPublication(t *testing.T) {
	f := newRecoveryFixture(t)
	f.exec("UPDATE sites SET desired_deployment_id=applied_deployment_id WHERE id='site-alpha'")
	f.exec("UPDATE publications SET status='FAILED' WHERE id=$1", f.publication.ID)
	f.exec("UPDATE jobs SET status='DONE' WHERE id='job-target'")
	if err := f.a.Doctor(f.ctx); err != nil {
		t.Fatalf("generation-only difference is legitimate after failed publication: %v", err)
	}
}

type recoveryResponse struct {
	status int
	body   []byte
	etag   string
}

type recoveryGateway struct {
	mu                       sync.Mutex
	server                   *httptest.Server
	current                  gateway.M
	revision                 int
	posts                    int
	ifMatch                  string
	applyFailure             int
	applyDrift               bool
	readFailureAfterApply    bool
	loseApplyResponse        bool
	configChangeAfterContent bool
	responses                map[string]recoveryResponse
}

func newRecoveryGateway(t *testing.T) *recoveryGateway {
	t.Helper()
	g := &recoveryGateway{current: gateway.M{}, responses: map[string]recoveryResponse{}}
	g.server = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.server.Close)
	return g
}

func recoveryConfigCopy(cfg gateway.M) gateway.M {
	var copy gateway.M
	if err := json.Unmarshal(jsonBytes(cfg), &copy); err != nil {
		panic(err)
	}
	return copy
}

func (g *recoveryGateway) setConfig(cfg gateway.M) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.current = recoveryConfigCopy(cfg)
	g.revision++
}

func (g *recoveryGateway) config() gateway.M {
	g.mu.Lock()
	defer g.mu.Unlock()
	return recoveryConfigCopy(g.current)
}

func (g *recoveryGateway) postCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.posts
}

func (g *recoveryGateway) lastIfMatch() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ifMatch
}

func (g *recoveryGateway) override(host string, response recoveryResponse) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.responses[host] = response
}

func (g *recoveryGateway) setApplyFailure(status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.applyFailure = status
}

func (g *recoveryGateway) setApplyDrift() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.applyDrift = true
}

func (g *recoveryGateway) setReadFailureAfterApply() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.readFailureAfterApply = true
}

func (g *recoveryGateway) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.URL.Path == "/config/" {
		// Caddy's concurrency token is opaque and distinct from the operator's
		// canonical config fingerprint. The implementation must preserve it.
		etag := fmt.Sprintf(`"gateway-revision-%d"`, g.revision)
		switch r.Method {
		case http.MethodGet:
			if g.readFailureAfterApply && g.posts > 0 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("ETag", etag)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(g.current)
		case http.MethodPost:
			g.posts++
			g.ifMatch = r.Header.Get("If-Match")
			if g.applyFailure != 0 {
				w.WriteHeader(g.applyFailure)
				return
			}
			if g.ifMatch == "" || g.ifMatch != etag {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			var cfg gateway.M
			if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			g.current = cfg
			g.revision++
			if g.applyDrift {
				g.current["unexpected"] = "readback drift"
			}
			if g.loseApplyResponse {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	server := "public"
	if strings.HasPrefix(r.URL.Path, "/probe/") {
		server = "probe"
	}
	response := recoveryRoutedContent(g.current, server, r.Host)
	if g.configChangeAfterContent && r.Host == "beta.recovery.test" {
		g.current["changed-during-verification"] = true
		g.revision++
	}
	if override, ok := g.responses[r.Host]; ok {
		if override.status != 0 {
			response.status = override.status
		}
		if override.body != nil {
			response.body = override.body
		}
		if override.etag != "" {
			response.etag = override.etag
		}
	}
	if response.etag != "" {
		w.Header().Set("ETag", response.etag)
	}
	w.WriteHeader(response.status)
	w.Write(response.body)
}

// Interpret the tiny subset of generated Caddy routes needed by these tests.
// Content follows the applied config, so changing an unrelated route cannot be
// hidden by a fixture that always returns the expected database content.
func recoveryRoutedContent(cfg gateway.M, server, host string) recoveryResponse {
	public, ok := recoveryLookup(cfg, "apps", "http", "servers", server).(map[string]any)
	if !ok {
		return recoveryResponse{status: http.StatusNotFound}
	}
	routes, _ := public["routes"].([]any)
	for _, item := range routes {
		route, _ := item.(map[string]any)
		matches, _ := route["match"].([]any)
		if len(matches) == 0 {
			continue
		}
		match, _ := matches[0].(map[string]any)
		hosts, _ := match["host"].([]any)
		if len(hosts) == 0 || fmt.Sprint(hosts[0]) != host {
			continue
		}
		handlers, _ := route["handle"].([]any)
		for _, item := range handlers {
			handler, _ := item.(map[string]any)
			if handler["handler"] == "static_response" {
				status, _ := handler["status_code"].(float64)
				return recoveryResponse{status: int(status)}
			}
		}
		root := recoveryFindHandler(route, "file_server", "root")
		entry := recoveryFindHandler(route, "rewrite", "uri")
		if root == "" || entry == "" {
			return recoveryResponse{status: http.StatusNotFound}
		}
		path := filepath.Join(root, strings.TrimPrefix(entry, "/"))
		body, err := os.ReadFile(path)
		if err != nil {
			return recoveryResponse{status: http.StatusNotFound}
		}
		etag, _ := os.ReadFile(path + ".drop-etag")
		return recoveryResponse{status: http.StatusOK, body: body, etag: string(etag)}
	}
	return recoveryResponse{status: http.StatusNotFound}
}

func recoveryLookup(value any, keys ...string) any {
	for _, key := range keys {
		m, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = m[key]
	}
	return value
}

func recoveryFindHandler(value any, handler, field string) string {
	switch v := value.(type) {
	case map[string]any:
		if v["handler"] == handler {
			if result, ok := v[field].(string); ok {
				return result
			}
		}
		for _, child := range v {
			if result := recoveryFindHandler(child, handler, field); result != "" {
				return result
			}
		}
	case []any:
		for _, child := range v {
			if result := recoveryFindHandler(child, handler, field); result != "" {
				return result
			}
		}
	}
	return ""
}
