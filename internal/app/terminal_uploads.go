package app

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// A canceled worker may still be finishing its 120-second hard deadline.
// Leave time for its exit before measuring storage that it could still change.
const terminalUploadQuietPeriod = 150 * time.Second

func (a *App) maintainTerminalUploads(ctx context.Context) {
	rows, err := a.DB.Query(ctx, `SELECT d.id FROM deployments d JOIN quota_reservations q ON q.deployment_id=d.id WHERE d.status IN('FAILED','CANCELED') AND d.updated_at<now()-interval '150 seconds' ORDER BY d.id`)
	if err != nil {
		a.Log.Warn("terminal upload accounting postponed", "error", err.Error())
		return
	}
	var ids []string
	for rows.Next() {
		var did string
		if err = rows.Scan(&did); err != nil {
			break
		}
		ids = append(ids, did)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		a.Log.Warn("terminal upload accounting postponed", "error", err.Error())
		return
	}
	for _, did := range ids {
		if err = a.reconcileTerminalUpload(ctx, did); err != nil {
			a.Log.Warn("terminal upload accounting postponed", "deploymentId", did, "error", err.Error())
		}
	}
}

// Keep the existing 24-hour payload cleanup independent of the backup GC lock.
// Accounting is updated separately only after all residual trees are measured.
func (a *App) cleanupTerminalUpload(ctx context.Context, did string) error {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(82943003)").Scan(&locked); err != nil || !locked {
		return err
	}
	var eligible bool
	if err = tx.QueryRow(ctx, `SELECT status IN('FAILED','CANCELED') AND updated_at<now()-interval '24 hours' AND NOT EXISTS(SELECT 1 FROM jobs WHERE resource_id=$1 AND kind='PROCESS' AND status IN('PENDING','RUNNING')) FROM deployments WHERE id=$1 FOR UPDATE`, did).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return nil
	}
	if _, err = terminalUploadPaths(a.Config.DataDir, did, false); err != nil {
		return err
	}
	if err = os.RemoveAll(filepath.Join(a.Config.DataDir, "uploads", did)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *App) reconcileTerminalUpload(ctx context.Context, did string) error {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, lock := range []int64{82943006, 82943003} {
		var locked bool
		if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", lock).Scan(&locked); err != nil || !locked {
			return err
		}
	}
	var eligible bool
	var artifactState string
	if err = tx.QueryRow(ctx, `SELECT status IN('FAILED','CANCELED') AND updated_at<now()-make_interval(secs=>$2) AND NOT EXISTS(SELECT 1 FROM jobs WHERE resource_id=$1 AND kind='PROCESS' AND status IN('PENDING','RUNNING')),artifact_state FROM deployments WHERE id=$1 FOR UPDATE`, did, terminalUploadQuietPeriod.Seconds()).Scan(&eligible, &artifactState); err != nil {
		return err
	}
	if !eligible {
		return nil
	}
	paths, err := terminalUploadPaths(a.Config.DataDir, did, artifactState != "AVAILABLE" && artifactState != "GC_PENDING")
	if err != nil {
		return err
	}
	var retained int64
	for _, path := range paths {
		n, err := terminalTreeBytes(path)
		if err != nil {
			return err
		}
		if n > math.MaxInt64-retained {
			return fmt.Errorf("terminal upload storage size overflow")
		}
		retained += n
	}
	if retained == 0 {
		_, err = tx.Exec(ctx, "DELETE FROM quota_reservations WHERE deployment_id=$1", did)
	} else {
		_, err = tx.Exec(ctx, "UPDATE quota_reservations SET reserved_bytes=$2 WHERE deployment_id=$1", did, retained)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func terminalUploadPaths(dataDir, did string, includeRelease bool) ([]string, error) {
	if did == "" || did == "." || did == ".." || filepath.Base(did) != did || strings.ContainsAny(did, `/\`) {
		return nil, fmt.Errorf("invalid deployment storage identifier")
	}
	paths := []string{filepath.Join(dataDir, "uploads", did)}
	for _, name := range []string{"uploads", "quarantine", "releases"} {
		info, err := os.Lstat(filepath.Join(dataDir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("unexpected deployment storage directory")
		}
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "quarantine"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), did+"-") || strings.HasPrefix(entry.Name(), "release-"+did+"-") {
			paths = append(paths, filepath.Join(dataDir, "quarantine", entry.Name()))
		}
	}
	if includeRelease {
		paths = append(paths, filepath.Join(dataDir, "releases", did))
	}
	return paths, nil
}

func terminalTreeBytes(path string) (int64, error) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	var total int64
	err := filepath.WalkDir(path, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in terminal upload storage")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("special file in terminal upload storage")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Blocks < 0 || stat.Blocks > math.MaxInt64/512 || info.Size() < 0 {
			return fmt.Errorf("unverifiable terminal upload storage size")
		}
		// Count sparse logical size as well as allocated blocks; add an inode/
		// directory allowance even when the filesystem reports zero blocks.
		n := max(info.Size(), stat.Blocks*512)
		if n > math.MaxInt64-4096 || n+4096 > math.MaxInt64-total {
			return fmt.Errorf("terminal upload storage size overflow")
		}
		total += n + 4096
		return nil
	})
	return total, err
}
