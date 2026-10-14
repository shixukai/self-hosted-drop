package app

import (
	"testing"

	"github.com/shixukai/self-hosted-drop/db/migrations"
)

func TestAutoPublicationUpgradePreservesAcceptedAndUnknownHistory(t *testing.T) {
	f := newRecoveryFixture(t)
	for _, d := range []Deployment{f.target, f.other} {
		f.exec("UPDATE deployments SET metadata=jsonb_set(metadata,'{publishAfterReady}','true'::jsonb) WHERE id=$1", d.ID)
		f.exec("INSERT INTO jobs(id,kind,resource_id,status) VALUES($1,'PROCESS',$2,'DONE')", "process-"+d.ID, d.ID)
	}
	f.exec("INSERT INTO idempotency_records(actor_id,operation,key,request_hash,resource_id) SELECT created_by,'publication:'||site_id,'auto-'||id,'historical-hash','publication-target' FROM deployments WHERE id=$1", f.target.ID)
	f.exec("DELETE FROM schema_migrations WHERE name='005_auto_publish_history.sql'")
	if e := migrations.Apply(f.ctx, f.a.DB); e != nil {
		t.Fatal(e)
	}
	accepted := readAutoDeployment(t, f, f.target.ID)
	unknown := readAutoDeployment(t, f, f.other.ID)
	unrequested := readAutoDeployment(t, f, f.old.ID)
	if accepted.AutoPublicationID == nil || *accepted.AutoPublicationID != f.publication.ID || accepted.AutoPublishErrorCode != "" {
		t.Fatal("accepted automatic publication history was lost")
	}
	if unknown.Status != "READY" || unknown.AutoPublicationID != nil || unknown.AutoPublishErrorCode != "AUTO_PUBLISH_OUTCOME_UNKNOWN" || unknown.AutoPublishErrorMessage == "" {
		t.Fatal("historical unknown outcome was incorrectly treated as pending or failed files")
	}
	if unrequested.AutoPublicationID != nil || unrequested.AutoPublishErrorCode != "" {
		t.Fatal("upgrade changed ordinary deployment")
	}
	var count int
	if e := f.a.DB.QueryRow(f.ctx, "SELECT count(*) FROM publications").Scan(&count); e != nil || count != 1 {
		t.Fatal("upgrade submitted an additional publication")
	}
}
