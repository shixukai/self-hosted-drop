CREATE TABLE administrators (singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), id text UNIQUE NOT NULL, password_hash text NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE admin_sessions (digest text PRIMARY KEY, administrator_id text NOT NULL REFERENCES administrators(id), expires_at timestamptz NOT NULL, touched_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE sites (
 id text PRIMARY KEY, slug text UNIQUE NOT NULL, name text NOT NULL CHECK(char_length(name) BETWEEN 1 AND 100), description text NOT NULL DEFAULT '',
 status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','ACTIVE','PAUSED','DELETED')),
 desired_status text NOT NULL DEFAULT 'DRAFT', generation bigint NOT NULL DEFAULT 0, applied_generation bigint NOT NULL DEFAULT 0,
 applied_deployment_id text, desired_deployment_id text, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), deleted_at timestamptz
);
CREATE TABLE deployments (
 id text PRIMARY KEY, site_id text NOT NULL REFERENCES sites(id), status text NOT NULL CHECK(status IN ('RECEIVING','QUEUED','VALIDATING','EXTRACTING','CHECKING','AWAITING_INPUT','READY','FAILED','CANCELED')),
 artifact_state text NOT NULL DEFAULT 'PENDING' CHECK(artifact_state IN ('PENDING','AVAILABLE','GC_PENDING','PURGED','CORRUPT')),
 input_hash text NOT NULL DEFAULT '', artifact_hash text NOT NULL DEFAULT '', filename text NOT NULL,
 metadata jsonb NOT NULL DEFAULT '{}', report jsonb NOT NULL DEFAULT '{}', files jsonb NOT NULL DEFAULT '[]',
 received_bytes bigint NOT NULL DEFAULT 0, expanded_bytes bigint NOT NULL DEFAULT 0, file_count integer NOT NULL DEFAULT 0,
 error_code text NOT NULL DEFAULT '', error_message text NOT NULL DEFAULT '', created_by text NOT NULL, pinned boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(site_id,id)
);
ALTER TABLE sites ADD CONSTRAINT applied_deployment_owner FOREIGN KEY(id,applied_deployment_id) REFERENCES deployments(site_id,id);
ALTER TABLE sites ADD CONSTRAINT desired_deployment_owner FOREIGN KEY(id,desired_deployment_id) REFERENCES deployments(site_id,id);
CREATE INDEX deployment_history ON deployments(site_id,created_at DESC,id);
CREATE TABLE publications (
 id text PRIMARY KEY, site_id text NOT NULL REFERENCES sites(id), deployment_id text,
 action text NOT NULL CHECK(action IN ('PUBLISH','ROLLBACK','PAUSE','RESUME','DELETE','RESTORE')),
 expected_generation bigint NOT NULL, target_generation bigint NOT NULL, target_status text NOT NULL,
 status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','APPLYING','APPLIED','FAILED','CONFLICT','RECOVERING','CANCELED')),
 previous_config jsonb, error_code text NOT NULL DEFAULT '', error_message text NOT NULL DEFAULT '',
 created_by text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), applied_at timestamptz,
 UNIQUE(site_id,target_generation), FOREIGN KEY(site_id,deployment_id) REFERENCES deployments(site_id,id)
);
CREATE UNIQUE INDEX publication_one_active ON publications(site_id) WHERE status IN ('PENDING','APPLYING','RECOVERING');
CREATE TABLE jobs (id text PRIMARY KEY, kind text NOT NULL, resource_id text NOT NULL, status text NOT NULL DEFAULT 'PENDING', attempt_id text, lease_until timestamptz, attempts integer NOT NULL DEFAULT 0, next_run_at timestamptz NOT NULL DEFAULT now(), created_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX job_one_active ON jobs(kind,resource_id) WHERE status IN ('PENDING','RUNNING');
CREATE INDEX job_claim ON jobs(status,next_run_at,lease_until);
CREATE TABLE api_tokens (id text PRIMARY KEY, name text NOT NULL, digest text UNIQUE NOT NULL, prefix text NOT NULL, scopes text[] NOT NULL, expires_at timestamptz NOT NULL, revoked_at timestamptz, last_used_at timestamptz, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE api_token_sites(token_id text REFERENCES api_tokens(id) ON DELETE CASCADE,site_id text REFERENCES sites(id),PRIMARY KEY(token_id,site_id));
CREATE TABLE preview_grants(id text PRIMARY KEY,deployment_id text NOT NULL REFERENCES deployments(id),digest text UNIQUE NOT NULL,expires_at timestamptz NOT NULL,redeemed_at timestamptz,revoked_at timestamptz);
CREATE TABLE preview_sessions(digest text PRIMARY KEY,grant_id text NOT NULL REFERENCES preview_grants(id) ON DELETE CASCADE,deployment_id text NOT NULL REFERENCES deployments(id),expires_at timestamptz NOT NULL);
CREATE TABLE quota_reservations(deployment_id text PRIMARY KEY REFERENCES deployments(id),reserved_bytes bigint NOT NULL CHECK(reserved_bytes>=0));
CREATE TABLE idempotency_records(actor_id text NOT NULL,operation text NOT NULL,key text NOT NULL,request_hash text NOT NULL,resource_id text NOT NULL,status text NOT NULL DEFAULT 'DONE',created_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(actor_id,operation,key));
CREATE TABLE audit_events(id bigserial PRIMARY KEY,actor text NOT NULL,action text NOT NULL,site_id text,resource_id text,request_id text NOT NULL DEFAULT '',result text NOT NULL DEFAULT 'OK',created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE gateway_snapshots(generation bigserial PRIMARY KEY,config jsonb NOT NULL,config_hash text NOT NULL,created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE system_settings(singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),revision bigint NOT NULL DEFAULT 1,value jsonb NOT NULL DEFAULT '{}');
INSERT INTO system_settings(singleton) VALUES(true);
CREATE TABLE backup_runs(id text PRIMARY KEY,status text NOT NULL,snapshot_id text,manifest_hash text,created_at timestamptz NOT NULL DEFAULT now(),completed_at timestamptz);
CREATE TABLE artifact_pins(deployment_id text NOT NULL REFERENCES deployments(id),kind text NOT NULL,reference_id text NOT NULL,expires_at timestamptz,PRIMARY KEY(deployment_id,kind,reference_id));
