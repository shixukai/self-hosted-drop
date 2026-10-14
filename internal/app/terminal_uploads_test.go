package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testWorstCaseReservation = int64(2 << 30)

func terminalReservationFixture(t *testing.T, age time.Duration) (*recoveryFixture, string) {
	t.Helper()
	f := newRecoveryFixture(t)
	did := "terminal-deployment"
	f.exec("INSERT INTO deployments(id,site_id,status,filename,created_by,received_bytes,updated_at) VALUES($1,'site-beta','FAILED','test.zip','test',1,now()-make_interval(secs=>$2))", did, age.Seconds())
	f.exec("INSERT INTO quota_reservations(deployment_id,reserved_bytes) VALUES($1,$2)", did, testWorstCaseReservation)
	return f, did
}

func writeTerminalFile(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(size); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
}

func readTerminalReservation(t *testing.T, f *recoveryFixture, did string) int64 {
	t.Helper()
	var size int64
	if err := f.a.DB.QueryRow(f.ctx, "SELECT reserved_bytes FROM quota_reservations WHERE deployment_id=$1", did).Scan(&size); err != nil {
		t.Fatal(err)
	}
	return size
}

func TestTerminalReservationCountsAllResidualTrees(t *testing.T) {
	f, did := terminalReservationFixture(t, 3*time.Minute)
	const fileSize = int64(1 << 20)
	paths := []string{
		filepath.Join(f.a.Config.DataDir, "uploads", did, "payload"),
		filepath.Join(f.a.Config.DataDir, "quarantine", did+"-old-attempt", "tree.html"),
		filepath.Join(f.a.Config.DataDir, "quarantine", "release-"+did+"-old-attempt", "public", "index.html"),
		filepath.Join(f.a.Config.DataDir, "releases", did, "public", "index.html"),
	}
	for _, path := range paths {
		writeTerminalFile(t, path, fileSize)
	}
	writeTerminalFile(t, filepath.Join(f.a.Config.DataDir, "quarantine", "unrelated-task", "payload"), 16<<20)
	if err := f.a.reconcileTerminalUpload(f.ctx, did); err != nil {
		t.Fatal(err)
	}
	size := readTerminalReservation(t, f, did)
	if size < int64(len(paths))*fileSize || size > 5<<20 {
		t.Fatalf("reservation did not count exactly the owned residual trees: %d", size)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("accounting removed retained payload: %v", err)
		}
	}
	if err := f.a.cleanupTerminalUpload(f.ctx, did); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Fatal("upload was removed before the existing 24-hour retention period")
	}
}

func TestTerminalReservationWaitsForQuiescence(t *testing.T) {
	for _, test := range []struct {
		name, status string
		age          time.Duration
		job          string
	}{
		{name: "recent failure", status: "FAILED", age: 149 * time.Second},
		{name: "pending process", status: "FAILED", age: 3 * time.Minute, job: "PENDING"},
		{name: "running process", status: "CANCELED", age: 3 * time.Minute, job: "RUNNING"},
		{name: "ready deployment", status: "READY", age: 3 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, did := terminalReservationFixture(t, test.age)
			f.exec("UPDATE deployments SET status=$2 WHERE id=$1", did, test.status)
			writeTerminalFile(t, filepath.Join(f.a.Config.DataDir, "uploads", did, "payload"), 1)
			if test.job != "" {
				f.exec("INSERT INTO jobs(id,kind,resource_id,status) VALUES('terminal-job','PROCESS',$1,$2)", did, test.job)
			}
			if err := f.a.reconcileTerminalUpload(f.ctx, did); err != nil {
				t.Fatal(err)
			}
			if size := readTerminalReservation(t, f, did); size != testWorstCaseReservation {
				t.Fatalf("reservation changed before quiescence: %d", size)
			}
		})
	}
}

func TestTerminalReservationLeavesUnknownStorageReserved(t *testing.T) {
	f, did := terminalReservationFixture(t, 3*time.Minute)
	dir := filepath.Join(f.a.Config.DataDir, "uploads", did)
	writeTerminalFile(t, filepath.Join(dir, "payload"), 1)
	if err := os.Symlink(filepath.Join(f.a.Config.DataDir, "missing"), filepath.Join(dir, "unknown-link")); err != nil {
		t.Fatal(err)
	}
	if err := f.a.reconcileTerminalUpload(f.ctx, did); err == nil {
		t.Fatal("symlink allowed a reservation to shrink")
	}
	if size := readTerminalReservation(t, f, did); size != testWorstCaseReservation {
		t.Fatalf("unverifiable storage changed reservation: %d", size)
	}
}

func TestTerminalReservationRetainsOnScanError(t *testing.T) {
	f, did := terminalReservationFixture(t, 3*time.Minute)
	writeTerminalFile(t, filepath.Join(f.a.Config.DataDir, "uploads", did, "payload"), 1)
	// A damaged storage layout must not be treated as an empty quarantine.
	writeTerminalFile(t, filepath.Join(f.a.Config.DataDir, "quarantine"), 1)
	if err := f.a.reconcileTerminalUpload(f.ctx, did); err == nil {
		t.Fatal("unreadable quarantine layout was treated as empty")
	}
	if size := readTerminalReservation(t, f, did); size != testWorstCaseReservation {
		t.Fatalf("scan failure changed reservation: %d", size)
	}
}

func TestTerminalReservationRespectsCoordinationLocks(t *testing.T) {
	for _, lock := range []int64{82943006, 82943003} {
		t.Run(map[int64]string{82943006: "backup or GC", 82943003: "quota"}[lock], func(t *testing.T) {
			f, did := terminalReservationFixture(t, 3*time.Minute)
			conn, err := f.a.DB.Acquire(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			if _, err = conn.Exec(f.ctx, "SELECT pg_advisory_lock($1)", lock); err != nil {
				t.Fatal(err)
			}
			defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lock)
			if err = f.a.reconcileTerminalUpload(f.ctx, did); err != nil {
				t.Fatal(err)
			}
			if size := readTerminalReservation(t, f, did); size != testWorstCaseReservation {
				t.Fatalf("accounting ignored coordination lock %d", lock)
			}
		})
	}
}

func TestTerminalReservationRechecksStateAfterRowLock(t *testing.T) {
	f, did := terminalReservationFixture(t, 3*time.Minute)
	tx, err := f.a.DB.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if _, err = tx.Exec(f.ctx, "SELECT id FROM deployments WHERE id=$1 FOR UPDATE", did); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- f.a.reconcileTerminalUpload(f.ctx, did) }()
	conn, err := f.a.DB.Acquire(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var free bool
		if err = conn.QueryRow(f.ctx, "SELECT pg_try_advisory_lock(82943006)").Scan(&free); err != nil {
			t.Fatal(err)
		}
		if !free {
			break
		}
		conn.Exec(f.ctx, "SELECT pg_advisory_unlock(82943006)")
		if time.Now().After(deadline) {
			t.Fatal("accounting did not reach its deployment-row recheck")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err = tx.Exec(f.ctx, "UPDATE deployments SET status='READY' WHERE id=$1", did); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	if size := readTerminalReservation(t, f, did); size != testWorstCaseReservation {
		t.Fatalf("accounting used the pre-lock FAILED state: %d", size)
	}
}

func TestTerminalCleanupKeepsResidualReservation(t *testing.T) {
	f, did := terminalReservationFixture(t, 25*time.Hour)
	upload := filepath.Join(f.a.Config.DataDir, "uploads", did, "payload")
	residual := filepath.Join(f.a.Config.DataDir, "quarantine", did+"-old-attempt", "payload")
	writeTerminalFile(t, upload, 1<<20)
	writeTerminalFile(t, residual, 1<<20)
	if err := f.a.cleanupTerminalUpload(f.ctx, did); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(upload); !os.IsNotExist(err) {
		t.Fatal("24-hour upload cleanup did not run")
	}
	if err := f.a.reconcileTerminalUpload(f.ctx, did); err != nil {
		t.Fatal(err)
	}
	if size := readTerminalReservation(t, f, did); size < 1<<20 || size > 2<<20 {
		t.Fatalf("cleanup released or failed to count quarantine residue: %d", size)
	}
	if err := os.RemoveAll(filepath.Dir(residual)); err != nil {
		t.Fatal(err)
	}
	if err := f.a.reconcileTerminalUpload(f.ctx, did); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.a.DB.QueryRow(f.ctx, "SELECT count(*) FROM quota_reservations WHERE deployment_id=$1", did).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("empty residual storage kept its reservation")
	}
}

func TestReadyAutoPublicationRetriesBeyondProcessingLimit(t *testing.T) {
	f := newRecoveryFixture(t)
	// Prevent the unrelated publication job from being claimed first.
	f.exec("UPDATE jobs SET next_run_at=now()+interval '1 day'")
	d := f.other
	d.Metadata.PublishAfterReady = true
	generation := int64(1)
	d.Metadata.ExpectedGeneration = &generation
	f.exec("UPDATE deployments SET metadata=$2 WHERE id=$1", d.ID, jsonBytes(d.Metadata))
	f.exec("INSERT INTO jobs(id,kind,resource_id,status,attempts) VALUES('auto-process-job','PROCESS',$1,'PENDING',3)", d.ID)
	f.exec(`CREATE FUNCTION fail_auto_acceptance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'temporary acceptance failure'; END $$;
		CREATE TRIGGER auto_acceptance_failure BEFORE INSERT ON publications FOR EACH ROW EXECUTE FUNCTION fail_auto_acceptance()`)
	if err := f.a.runJob(f.ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := f.a.DB.QueryRow(f.ctx, "SELECT status FROM jobs WHERE id='auto-process-job'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	stored := readAutoDeployment(t, f, d.ID)
	if status != "PENDING" || stored.Status != "READY" || stored.AutoPublicationID != nil || stored.AutoPublishErrorCode != "" {
		t.Fatalf("transient automatic publication failure stopped retrying: job=%s deployment=%s code=%s", status, stored.Status, stored.AutoPublishErrorCode)
	}
	f.exec("DROP TRIGGER auto_acceptance_failure ON publications")
	f.exec("UPDATE jobs SET next_run_at=now() WHERE id='auto-process-job'")
	if err := f.a.runJob(f.ctx); err != nil {
		t.Fatal(err)
	}
	stored = readAutoDeployment(t, f, d.ID)
	if stored.AutoPublicationID == nil || stored.AutoPublishErrorCode != "" || stored.Status != "READY" {
		t.Fatal("automatic publication did not recover on the next process retry")
	}
}
