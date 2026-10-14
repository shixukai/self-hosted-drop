ALTER TABLE deployments ADD COLUMN storage_bytes bigint NOT NULL DEFAULT 0 CHECK(storage_bytes>=0);
UPDATE deployments SET storage_bytes=expanded_bytes+file_count*66+octet_length(report::text) WHERE artifact_state='AVAILABLE';
ALTER TABLE publications ADD COLUMN revert_required boolean NOT NULL DEFAULT false;
CREATE INDEX live_preview_grants ON preview_grants(deployment_id,expires_at) WHERE revoked_at IS NULL;
CREATE INDEX live_artifact_pins ON artifact_pins(deployment_id,expires_at);
