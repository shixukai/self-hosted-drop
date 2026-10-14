package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shixukai/self-hosted-drop/internal/archive"
	"github.com/shixukai/self-hosted-drop/internal/gateway"
)

// Backup freezes a database snapshot while immutable artifacts are protected from GC.
// Restic encrypts the resulting snapshot, including the database and internal keys.
func (a *App) Backup(ctx context.Context) (err error) {
	if os.Getenv("RESTIC_REPOSITORY") == "" || (os.Getenv("RESTIC_PASSWORD_FILE") == "" && os.Getenv("RESTIC_PASSWORD") == "") {
		return fmt.Errorf("RESTIC_REPOSITORY and RESTIC_PASSWORD_FILE are required")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	conn, e := a.DB.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	for _, lock := range []int64{82943005, 82943006} {
		var ok bool
		if e = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lock).Scan(&ok); e != nil {
			return e
		}
		if !ok {
			return fmt.Errorf("publication, backup or GC in progress")
		}
		defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lock)
	}
	var uncertain bool
	if e = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM publications WHERE status IN('APPLYING','RECOVERING'))").Scan(&uncertain); e != nil {
		return e
	}
	if uncertain {
		return fmt.Errorf("gateway recovery must finish before backup")
	}
	bid := id()
	if _, e = conn.Exec(ctx, "INSERT INTO backup_runs(id,status) VALUES($1,'RUNNING')", bid); e != nil {
		return e
	}
	defer func() {
		if err != nil {
			c, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			a.DB.Exec(c, "UPDATE backup_runs SET status='FAILED',completed_at=now() WHERE id=$1", bid)
		}
	}()
	stage := filepath.Join(a.Config.DataDir, "runtime", "backup-"+bid)
	if e = os.Mkdir(stage, 0700); e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	tx, e := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var snapshot string
	if e = tx.QueryRow(ctx, "SELECT pg_export_snapshot()").Scan(&snapshot); e != nil {
		return e
	}
	rows, e := tx.Query(ctx, "SELECT "+deploymentFields+" FROM deployments WHERE artifact_state='AVAILABLE' ORDER BY id")
	if e != nil {
		return e
	}
	deps := []Deployment{}
	for rows.Next() {
		d, e := scanDeployment(rows)
		if e != nil {
			rows.Close()
			return e
		}
		deps = append(deps, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	cfg, _, e := gateway.Read(ctx, a.Client, a.Config.CaddyAdmin)
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(stage, "gateway.json"), jsonBytes(cfg), 0600); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(stage, "credentials.json"), jsonBytes(map[string]string{"internalKey": a.Config.InternalKey, "workerKey": a.Config.WorkerKey, "adminOrigin": a.Config.AdminOrigin, "contentDomain": a.Config.ContentDomain}), 0600); e != nil {
		return e
	}
	// Keep credentials out of pg_dump's command line and child output.
	u, e := url.Parse(a.Config.DatabaseURL)
	if e != nil {
		return e
	}
	pgEnv := os.Environ()
	password, _ := u.User.Password()
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	ssl := u.Query().Get("sslmode")
	if ssl == "" {
		ssl = "prefer"
	}
	pgEnv = append(pgEnv, "PGHOST="+u.Hostname(), "PGPORT="+port, "PGUSER="+u.User.Username(), "PGPASSWORD="+password, "PGDATABASE="+strings.TrimPrefix(u.Path, "/"), "PGSSLMODE="+ssl)
	dump := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--no-owner", "--no-acl", "--snapshot="+snapshot, "--file="+filepath.Join(stage, "database.dump"))
	dump.Env = pgEnv
	if e = dump.Run(); e != nil {
		return fmt.Errorf("pg_dump failed; verify database connectivity and client version")
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	// Gateway changes may resume now; GC remains excluded until the snapshot is durable.
	if _, e = conn.Exec(ctx, "SELECT pg_advisory_unlock(82943005)"); e != nil {
		return e
	}
	manifest := map[string]string{}
	for _, d := range deps {
		source := filepath.Join(a.Config.DataDir, "releases", d.ID)
		if e = archive.Verify(filepath.Join(source, "public"), d.Report); e != nil {
			return e
		}
		target := filepath.Join(stage, "releases", d.ID)
		e = filepath.WalkDir(source, func(p string, f fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(source, p)
			if err != nil {
				return err
			}
			out := filepath.Join(target, rel)
			if f.IsDir() {
				return os.MkdirAll(out, 0750)
			}
			if !f.Type().IsRegular() {
				return fmt.Errorf("unexpected file type")
			}
			return os.Link(p, out)
		})
		if e != nil {
			return e
		}
		manifest[d.ID] = d.Report.ArtifactHash
	}
	if e = os.WriteFile(filepath.Join(stage, "backup-manifest.json"), jsonBytes(map[string]any{"version": 1, "backupId": bid, "createdAt": time.Now().UTC(), "artifacts": manifest}), 0600); e != nil {
		return e
	}
	backup := exec.CommandContext(ctx, "restic", "backup", "--json", "--tag", "drop:"+bid, ".")
	backup.Dir = stage
	resticEnv := append(os.Environ(), "RESTIC_CACHE_DIR="+filepath.Join(a.Config.DataDir, "runtime", "restic-cache"))
	backup.Env = resticEnv
	output, e := backup.Output()
	if e != nil {
		return fmt.Errorf("restic backup failed; verify repository and credentials")
	}
	var snapshotID string
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var line struct {
			Type     string `json:"message_type"`
			Snapshot string `json:"snapshot_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) == nil && line.Type == "summary" {
			snapshotID = line.Snapshot
		}
	}
	if snapshotID == "" {
		return fmt.Errorf("restic did not confirm a snapshot")
	}
	check := exec.CommandContext(ctx, "restic", "check", "--read-data")
	check.Env = resticEnv
	if e = check.Run(); e != nil {
		return fmt.Errorf("restic snapshot created but repository verification failed")
	}
	_, e = conn.Exec(ctx, "UPDATE backup_runs SET status='COMPLETED',snapshot_id=$2,manifest_hash=$3,completed_at=now() WHERE id=$1", bid, snapshotID, archive.Sum(jsonBytes(manifest)))
	if e != nil {
		return e
	}
	a.auditEvent(ctx, "operator", "BACKUP", "", bid, "COMPLETED")
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"backupId": bid, "snapshotId": snapshotID, "artifacts": len(deps), "verified": true})
}
func (a *App) Doctor(ctx context.Context) error {
	if e := a.DB.Ping(ctx); e != nil {
		return e
	}
	cfg, _, e := gateway.Read(ctx, a.Client, a.Config.CaddyAdmin)
	if e != nil {
		return e
	}
	rows, e := a.DB.Query(ctx, "SELECT "+siteFields+" FROM sites WHERE status<>'DRAFT'")
	if e != nil {
		return e
	}
	sites := []Site{}
	for rows.Next() {
		s, e := scanSite(rows)
		if e != nil {
			rows.Close()
			return e
		}
		sites = append(sites, s)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, s := range sites {
		if !gateway.HasRoute(cfg, fmt.Sprintf("site-%s-g%d", s.ID, s.AppliedGeneration)) {
			return fmt.Errorf("gateway route differs for site %s", s.Slug)
		}
		if s.Status == "ACTIVE" {
			d, e := scanDeployment(a.DB.QueryRow(ctx, "SELECT "+deploymentFields+" FROM deployments WHERE id=$1", s.DeploymentID))
			if e != nil {
				return e
			}
			if e = archive.Verify(filepath.Join(a.Config.DataDir, "releases", d.ID, "public"), d.Report); e != nil {
				return e
			}
		}
		if e = a.probePublication(ctx, Publication{SiteID: s.ID, TargetStatus: s.Status, DeploymentID: s.DeploymentID}); e != nil {
			return fmt.Errorf("site %s: %w", s.Slug, e)
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"database": "ok", "gateway": "ok", "verifiedSites": len(sites)})
}
