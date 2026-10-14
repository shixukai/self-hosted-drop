package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/shixukai/self-hosted-drop/internal/archive"
)

func TestResumeSelectedDeploymentUnblocksPausedSites(t *testing.T) {
	for _, condition := range []string{"no applied version", "purged applied version", "missing applied files", "acknowledged warnings"} {
		t.Run(condition, func(t *testing.T) {
			f := newRecoveryFixture(t)
			selected := f.artifact("resume-selected", "site-paused", "<html>replacement release</html>")
			if condition == "purged applied version" || condition == "missing applied files" {
				old := f.artifact("resume-old", "site-paused", "<html>old release</html>")
				f.exec("UPDATE sites SET applied_deployment_id=$2,desired_deployment_id=$2 WHERE id=$1", "site-paused", old.ID)
				if condition == "purged applied version" {
					f.exec("UPDATE deployments SET artifact_state='PURGED' WHERE id=$1", old.ID)
				} else if err := os.RemoveAll(filepath.Join(f.a.Config.DataDir, "releases", old.ID)); err != nil {
					t.Fatal(err)
				}
			}
			if condition == "acknowledged warnings" {
				selected.Report.Warnings = []archive.Warning{{Code: "MISSING_RESOURCE", Message: "Missing test asset"}}
				f.exec("UPDATE deployments SET report=$2 WHERE id=$1", selected.ID, jsonBytes(selected.Report))
			}
			generation := int64(1)
			response := submitResume(f, "site-paused", Actor{ID: "test-admin", Admin: true}, publishRequest{ExpectedGeneration: &generation, DeploymentID: selected.ID, ReportHash: selected.Report.ReportHash, AcknowledgeWarnings: true})
			if response.Code != 202 {
				t.Fatalf("selected resume rejected a replacement: status=%d body=%s", response.Code, response.Body.String())
			}
			var publication Publication
			if err := json.Unmarshal(response.Body.Bytes(), &publication); err != nil {
				t.Fatal(err)
			}
			if publication.Action != "RESUME" || publication.TargetStatus != "ACTIVE" || publication.DeploymentID == nil || *publication.DeploymentID != selected.ID {
				t.Fatalf("resume did not target the selected artifact: %+v", publication)
			}
			site, err := scanSite(f.a.DB.QueryRow(f.ctx, "SELECT "+siteFields+" FROM sites WHERE id='site-paused'"))
			if err != nil {
				t.Fatal(err)
			}
			if site.Status != "PAUSED" || site.DesiredStatus != "ACTIVE" || site.Generation != 2 || site.DesiredDeploymentID == nil || *site.DesiredDeploymentID != selected.ID {
				t.Fatalf("acceptance bypassed the normal site transition: %+v", site)
			}
		})
	}
}

func TestResumeSelectedDeploymentRequiresManagementAndInspection(t *testing.T) {
	for _, test := range []struct {
		name, code                                string
		status                                    int
		publicationOnly, wrongSite                bool
		wrongHash, warnings, unready, unavailable bool
	}{
		{name: "publication-only token", publicationOnly: true, status: 403, code: "FORBIDDEN"},
		{name: "different site", wrongSite: true, status: 404, code: "NOT_FOUND"},
		{name: "changed report", wrongHash: true, status: 409, code: "INSPECTION_CHANGED"},
		{name: "unacknowledged warnings", warnings: true, status: 422, code: "WARNINGS_NOT_ACKNOWLEDGED"},
		{name: "unready artifact", unready: true, status: 409, code: "ARTIFACT_UNAVAILABLE"},
		{name: "unavailable artifact", unavailable: true, status: 409, code: "ARTIFACT_UNAVAILABLE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newRecoveryFixture(t)
			selected := f.artifact("resume-selected", "site-paused", "<html>replacement release</html>")
			generation := int64(1)
			req := publishRequest{ExpectedGeneration: &generation, DeploymentID: selected.ID, ReportHash: selected.Report.ReportHash}
			who := Actor{ID: "test-admin", Admin: true}
			if test.publicationOnly {
				who = Actor{ID: "token:resume-token", TokenID: "resume-token", Scopes: []string{"publications:write"}}
			}
			if test.wrongSite {
				req.DeploymentID, req.ReportHash = f.other.ID, f.other.Report.ReportHash
			}
			if test.wrongHash {
				req.ReportHash = "stale-inspection-hash"
			}
			if test.warnings {
				selected.Report.Warnings = []archive.Warning{{Code: "MISSING_RESOURCE", Message: "Missing test asset"}}
				f.exec("UPDATE deployments SET report=$2 WHERE id=$1", selected.ID, jsonBytes(selected.Report))
			}
			if test.unready {
				f.exec("UPDATE deployments SET status='CHECKING' WHERE id=$1", selected.ID)
			}
			if test.unavailable {
				f.exec("UPDATE deployments SET artifact_state='PURGED' WHERE id=$1", selected.ID)
			}
			response := submitResume(f, "site-paused", who, req)
			var rejection APIError
			if err := json.Unmarshal(response.Body.Bytes(), &rejection); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status || rejection.Code != test.code {
				t.Fatalf("unexpected resume validation: status=%d code=%s, want %d/%s", response.Code, rejection.Code, test.status, test.code)
			}
			var generationAfter int64
			var publications int
			if err := f.a.DB.QueryRow(f.ctx, "SELECT generation,(SELECT count(*) FROM publications WHERE site_id='site-paused') FROM sites WHERE id='site-paused'").Scan(&generationAfter, &publications); err != nil {
				t.Fatal(err)
			}
			if generationAfter != generation || publications != 0 {
				t.Fatal("rejected resume changed the site or created a publication")
			}
		})
	}
}

func TestResumeWithoutSelectionKeepsExistingVersionRules(t *testing.T) {
	for _, state := range []string{"none", "AVAILABLE", "PURGED"} {
		t.Run(state, func(t *testing.T) {
			f := newRecoveryFixture(t)
			var old Deployment
			if state != "none" {
				old = f.artifact("resume-old", "site-paused", "<html>old release</html>")
				f.exec("UPDATE sites SET applied_deployment_id=$2,desired_deployment_id=$2 WHERE id=$1", "site-paused", old.ID)
				f.exec("UPDATE deployments SET artifact_state=$2 WHERE id=$1", old.ID, state)
			}
			generation := int64(1)
			response := submitResume(f, "site-paused", Actor{ID: "test-admin", Admin: true}, publishRequest{ExpectedGeneration: &generation})
			if state == "AVAILABLE" {
				var publication Publication
				if err := json.Unmarshal(response.Body.Bytes(), &publication); err != nil {
					t.Fatal(err)
				}
				if response.Code != 202 || publication.DeploymentID == nil || *publication.DeploymentID != old.ID {
					t.Fatal("default resume did not preserve the applied version")
				}
			} else {
				var rejection APIError
				if err := json.Unmarshal(response.Body.Bytes(), &rejection); err != nil {
					t.Fatal(err)
				}
				want := "ARTIFACT_UNAVAILABLE"
				if state == "none" {
					want = "SITE_NOT_PUBLISHED"
				}
				if response.Code != 409 || rejection.Code != want {
					t.Fatalf("default resume rules changed: status=%d code=%s", response.Code, rejection.Code)
				}
			}
		})
	}
}

func submitResume(f *recoveryFixture, sid string, who Actor, req publishRequest) *httptest.ResponseRecorder {
	f.t.Helper()
	route := chi.NewRouteContext()
	route.URLParams.Add("site", sid)
	ctx := context.WithValue(f.ctx, chi.RouteCtxKey, route)
	ctx = context.WithValue(ctx, actorKey{}, who)
	request := httptest.NewRequest("POST", "/api/v1/sites/"+sid+"/resume", bytes.NewReader(jsonBytes(req))).WithContext(ctx)
	response := httptest.NewRecorder()
	f.a.handle(f.a.siteOperation("resume"))(response, request)
	return response
}
