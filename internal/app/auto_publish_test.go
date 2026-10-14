package app

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/shixukai/self-hosted-drop/internal/archive"
)

func TestAutoPublicationRefusalsRemainReady(t *testing.T) {
	for _, test := range []struct {
		name, siteID, code                  string
		revoked, forbidden, stale, warnings bool
	}{
		{name: "paused", siteID: "site-paused", code: "SITE_PAUSED"},
		{name: "stale generation", siteID: "site-beta", stale: true, code: "DEPLOYMENT_CONFLICT"},
		{name: "revoked token", siteID: "site-beta", revoked: true, code: "TOKEN_REVOKED"},
		{name: "removed permission", siteID: "site-beta", forbidden: true, code: "FORBIDDEN"},
		{name: "unacknowledged warnings", siteID: "site-beta", warnings: true, code: "WARNINGS_NOT_ACKNOWLEDGED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newRecoveryFixture(t)
			d := f.artifact("deployment-auto", test.siteID, "<html>automatic publication</html>")
			generation := int64(1)
			if test.stale {
				generation = 0
			}
			d.CreatedBy = "token:auto-token"
			d.Metadata = archive.Metadata{PublishAfterReady: true, ExpectedGeneration: &generation}
			if test.warnings {
				d.Report.Warnings = []archive.Warning{{Code: "MISSING_RESOURCE", Message: "Missing test asset"}}
			}
			permissions := []string{"publications:write"}
			if test.forbidden {
				permissions = []string{"deployments:write"}
			}
			f.exec("INSERT INTO api_tokens(id,name,digest,prefix,scopes,expires_at,revoked_at) VALUES('auto-token','test','test-digest','test-prefix',$1,now()+interval '1 day',CASE WHEN $2 THEN now() ELSE NULL END)", permissions, test.revoked)
			f.exec("INSERT INTO api_token_sites(token_id,site_id) VALUES('auto-token',$1)", d.SiteID)
			f.exec("UPDATE deployments SET created_by=$2,metadata=$3,report=$4 WHERE id=$1", d.ID, d.CreatedBy, jsonBytes(d.Metadata), jsonBytes(d.Report))
			if err := f.a.autoPublish(f.ctx, d, d.Report); err != nil {
				t.Fatalf("semantic refusal became a processing error: %v", err)
			}
			stored := readAutoDeployment(t, f, d.ID)
			if stored.Status != "READY" || stored.AutoPublicationID != nil || stored.AutoPublishErrorCode != test.code || stored.AutoPublishErrorMessage == "" {
				t.Fatalf("missing separate automatic-publication refusal: status=%s publication=%v code=%s", stored.Status, stored.AutoPublicationID, stored.AutoPublishErrorCode)
			}
			var publications, audits int
			if err := f.a.DB.QueryRow(f.ctx, "SELECT (SELECT count(*) FROM publications WHERE deployment_id=$1),(SELECT count(*) FROM audit_events WHERE resource_id=$1 AND action='AUTO_PUBLISH' AND result=$2)", d.ID, test.code).Scan(&publications, &audits); err != nil {
				t.Fatal(err)
			}
			if publications != 0 || audits != 1 {
				t.Fatalf("refusal created a publication or lacked audit: publications=%d audits=%d", publications, audits)
			}
		})
	}
}

func TestAutoPublicationResultRetryReusesAcceptedPublication(t *testing.T) {
	f := newRecoveryFixture(t)
	d := f.other
	generation := int64(1)
	d.CreatedBy = "token:auto-token"
	d.Metadata = archive.Metadata{PublishAfterReady: true, ExpectedGeneration: &generation}
	f.exec("INSERT INTO api_tokens(id,name,digest,prefix,scopes,expires_at) VALUES('auto-token','test','test-digest','test-prefix',ARRAY['publications:write'],now()+interval '1 day')")
	f.exec("INSERT INTO api_token_sites(token_id,site_id) VALUES('auto-token',$1)", d.SiteID)
	f.exec("UPDATE deployments SET created_by=$2,metadata=$3 WHERE id=$1", d.ID, d.CreatedBy, jsonBytes(d.Metadata))
	f.exec(`CREATE FUNCTION fail_auto_result() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN RAISE EXCEPTION 'injected result persistence failure'; END $$;
	CREATE TRIGGER auto_result_failure BEFORE UPDATE OF auto_publication_id ON deployments FOR EACH ROW EXECUTE FUNCTION fail_auto_result()`)
	if err := f.a.autoPublish(f.ctx, d, d.Report); err == nil {
		t.Fatal("result persistence failure was swallowed")
	}
	var acceptedID string
	if err := f.a.DB.QueryRow(f.ctx, "SELECT id FROM publications WHERE deployment_id=$1", d.ID).Scan(&acceptedID); err != nil {
		t.Fatalf("publication acceptance did not commit before injected failure: %v", err)
	}
	failedWrite := readAutoDeployment(t, f, d.ID)
	if failedWrite.AutoPublicationID != nil || failedWrite.AutoPublishErrorCode != "" {
		t.Fatal("transient persistence failure became a semantic refusal")
	}
	f.exec("DROP TRIGGER auto_result_failure ON deployments")
	f.exec("UPDATE api_tokens SET revoked_at=now() WHERE id='auto-token'")
	if err := f.a.autoPublish(f.ctx, failedWrite, failedWrite.Report); err != nil {
		t.Fatalf("accepted publication recovery failed: %v", err)
	}
	stored := readAutoDeployment(t, f, d.ID)
	if stored.AutoPublicationID == nil || *stored.AutoPublicationID != acceptedID || stored.AutoPublishErrorCode != "" || stored.AutoPublishErrorMessage != "" || stored.Status != "READY" {
		t.Fatalf("retry lost the accepted publication outcome: status=%s code=%s publication=%v", stored.Status, stored.AutoPublishErrorCode, stored.AutoPublicationID)
	}
	// A replay with the stale deployment snapshot must not duplicate acceptance
	// or its result audit after the first durable result has been written.
	if err := f.a.autoPublish(f.ctx, failedWrite, failedWrite.Report); err != nil {
		t.Fatal(err)
	}
	var publications, audits int
	if err := f.a.DB.QueryRow(f.ctx, "SELECT (SELECT count(*) FROM publications WHERE deployment_id=$1),(SELECT count(*) FROM audit_events WHERE resource_id=$1 AND action='AUTO_PUBLISH' AND result='ACCEPTED')", d.ID).Scan(&publications, &audits); err != nil {
		t.Fatal(err)
	}
	if publications != 1 || audits != 1 {
		t.Fatalf("retry duplicated acceptance or audit: publications=%d audits=%d", publications, audits)
	}
}

func TestAutoPublicationTransientErrorsRemainRetryable(t *testing.T) {
	a := &App{}
	for _, err := range []error{
		context.DeadlineExceeded,
		errors.New("temporary database failure"),
		apiError(http.StatusServiceUnavailable, "TEMPORARY_FAILURE", "try again"),
		&APIError{Status: http.StatusConflict, Code: "TRY_AGAIN", Retryable: true},
	} {
		if got := a.autoPublicationError(context.Background(), Deployment{}, err); !errors.Is(got, err) {
			t.Fatalf("transient error was swallowed: got %v, want %v", got, err)
		}
	}
}

func readAutoDeployment(t *testing.T, f *recoveryFixture, did string) Deployment {
	t.Helper()
	d, err := scanDeployment(f.a.DB.QueryRow(f.ctx, "SELECT "+deploymentFields+" FROM deployments WHERE id=$1", did))
	if err != nil {
		t.Fatal(err)
	}
	return d
}
