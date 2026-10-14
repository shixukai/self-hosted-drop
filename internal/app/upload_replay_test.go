package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestUploadReplayDoesNotCreateDeploymentHistory(t *testing.T) {
	for _, state := range []string{"QUEUED", "FAILED", "CANCELED"} {
		t.Run(state, func(t *testing.T) {
			f := newUploadReplayFixture(t)
			original, err := submitReplayUpload(f, "stable-key", "<html>original</html>")
			if err != nil {
				t.Fatal(err)
			}
			if state != "QUEUED" {
				f.exec("UPDATE deployments SET status=$2 WHERE id=$1", original.ID, state)
				f.exec("UPDATE jobs SET status='DONE' WHERE resource_id=$1 AND kind='PROCESS'", original.ID)
			}
			replay, err := submitReplayUpload(f, "stable-key", "<html>original</html>")
			if err != nil {
				t.Fatal(err)
			}
			if replay.ID != original.ID || replay.Status != state {
				t.Fatalf("replay changed the original resource: id=%s status=%s", replay.ID, replay.Status)
			}
			assertReplayStorage(t, f, 1, 1, 1)
			if state != "QUEUED" {
				fresh, err := submitReplayUpload(f, "fresh-attempt-key", "<html>original</html>")
				if err != nil {
					t.Fatal(err)
				}
				if fresh.ID == original.ID || fresh.Status != "QUEUED" {
					t.Fatal("a fresh explicit key did not create exactly one new attempt")
				}
				assertReplayStorage(t, f, 2, 2, 2)
			}
		})
	}
}

func TestUploadReplayStillVerifiesPayloadFingerprint(t *testing.T) {
	f := newUploadReplayFixture(t)
	original, err := submitReplayUpload(f, "stable-key", "<html>original</html>")
	if err != nil {
		t.Fatal(err)
	}
	_, err = submitReplayUpload(f, "stable-key", "<html>different</html>")
	var rejection *APIError
	if !errors.As(err, &rejection) || rejection.Status != 409 || rejection.Code != "IDEMPOTENCY_MISMATCH" {
		t.Fatalf("replay bypassed body verification: %v", err)
	}
	// A different payload is a real failed receipt, unlike a matching replay.
	assertReplayStorage(t, f, 2, 1, 1)
	replay, err := submitReplayUpload(f, "stable-key", "<html>original</html>")
	if err != nil || replay.ID != original.ID {
		t.Fatalf("mismatch replaced the original key: id=%s err=%v", replay.ID, err)
	}
	assertReplayStorage(t, f, 2, 1, 1)
}

func newUploadReplayFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	f := newRecoveryFixture(t)
	f.a.Config.MaxUpload = 1 << 20
	f.a.Config.MaxExpanded = 1 << 20
	f.a.Config.Quota = 64 << 20
	f.a.Config.QueueLimit = 10
	if err := os.MkdirAll(filepath.Join(f.a.Config.DataDir, "uploads"), 0750); err != nil {
		t.Fatal(err)
	}
	return f
}

func submitReplayUpload(f *recoveryFixture, key, payload string) (Deployment, error) {
	f.t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadata, err := writer.CreateFormField("metadata")
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err = metadata.Write([]byte(`{"interactionMode":"strict","routingMode":"static"}`)); err != nil {
		f.t.Fatal(err)
	}
	file, err := writer.CreateFormFile("file", "index.html")
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err = file.Write([]byte(payload)); err != nil {
		f.t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		f.t.Fatal(err)
	}
	route := chi.NewRouteContext()
	route.URLParams.Add("site", "site-beta")
	ctx := context.WithValue(f.ctx, chi.RouteCtxKey, route)
	ctx = context.WithValue(ctx, actorKey{}, Actor{ID: "test-admin", Admin: true})
	request := httptest.NewRequest("POST", "/api/v1/sites/site-beta/deployments", &body).WithContext(ctx)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	if err = f.a.upload(response, request); err != nil {
		return Deployment{}, err
	}
	if response.Code != 202 {
		f.t.Fatalf("unexpected upload status: %d", response.Code)
	}
	var deployment Deployment
	if err = json.Unmarshal(response.Body.Bytes(), &deployment); err != nil {
		f.t.Fatal(err)
	}
	return deployment, nil
}

func assertReplayStorage(t *testing.T, f *recoveryFixture, deployments, reservations, uploadDirs int) {
	t.Helper()
	var gotDeployments, gotReservations int
	if err := f.a.DB.QueryRow(f.ctx, "SELECT (SELECT count(*) FROM deployments WHERE site_id='site-beta' AND id<>'deployment-other'),(SELECT count(*) FROM quota_reservations)").Scan(&gotDeployments, &gotReservations); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(f.a.Config.DataDir, "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	if gotDeployments != deployments || gotReservations != reservations || len(entries) != uploadDirs {
		t.Fatalf("unexpected replay residue: deployments=%d reservations=%d directories=%d, want %d/%d/%d", gotDeployments, gotReservations, len(entries), deployments, reservations, uploadDirs)
	}
}
