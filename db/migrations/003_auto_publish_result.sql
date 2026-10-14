ALTER TABLE deployments ADD COLUMN auto_publication_id text REFERENCES publications(id);
ALTER TABLE deployments ADD COLUMN auto_publish_error_code text NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN auto_publish_error_message text NOT NULL DEFAULT '';
