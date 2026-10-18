-- SPDX-License-Identifier: AGPL-3.0-only
-- Expansion only: existing event rows are preserved. Consumers flag evidence
-- with no matching authenticated run as legacy_unverified, never as approval.
SET LOCAL lock_timeout = '5s';
CREATE TABLE review_runs (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 ticket_node_id uuid NOT NULL,
 project_id uuid NOT NULL,
 repository text NOT NULL,
 pull_request bigint NOT NULL,
 target_branch text NOT NULL,
 base_sha text NOT NULL,
 head_sha text NOT NULL,
 scope text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('review','visual')),
 author_session_id uuid NOT NULL,
 reviewer_session_id uuid NOT NULL,
 reviewer_principal_id uuid NOT NULL,
 author_family text NOT NULL,
 reviewer_family text NOT NULL,
 harness text NOT NULL,
 model text NOT NULL,
 reasoning_effort text NOT NULL,
 created_by uuid NOT NULL,
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 credential_digest bytea,
 credential_key_id uuid,
 verdict text,
 visual_impact text,
 reason text,
 submitted_at timestamptz,
 PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,ticket_node_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY(tenant_id,author_session_id) REFERENCES harness_sessions(tenant_id,id),
 FOREIGN KEY(tenant_id,reviewer_session_id) REFERENCES harness_sessions(tenant_id,id),
 FOREIGN KEY(tenant_id,reviewer_principal_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY(tenant_id,created_by) REFERENCES principals(tenant_id,id),
 FOREIGN KEY(tenant_id,credential_key_id) REFERENCES agent_keys(tenant_id,id)
);
CREATE INDEX review_runs_ticket ON review_runs(tenant_id,ticket_node_id,created_at DESC,id);
CREATE UNIQUE INDEX review_runs_credential ON review_runs(tenant_id,credential_digest) WHERE credential_digest IS NOT NULL;
ALTER TABLE review_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE review_runs FORCE ROW LEVEL SECURITY;
-- Capability lookup is tenant-only. Every project read/write is authorized by
-- the store; no run or credential listing is exposed to unauthenticated callers.
CREATE POLICY review_runs_tenant ON review_runs
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

CREATE FUNCTION aeon_review_run_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (to_jsonb(OLD)-ARRAY['credential_digest','credential_key_id','verdict','visual_impact','reason','submitted_at'])
    IS DISTINCT FROM (to_jsonb(NEW)-ARRAY['credential_digest','credential_key_id','verdict','visual_impact','reason','submitted_at'])
    OR OLD.submitted_at IS NOT NULL
    OR (OLD.credential_digest IS NOT NULL AND
        (OLD.credential_digest IS DISTINCT FROM NEW.credential_digest OR OLD.credential_key_id IS DISTINCT FROM NEW.credential_key_id)) THEN
   RAISE EXCEPTION 'review run binding and submitted verdict are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER review_run_immutable BEFORE UPDATE ON review_runs
 FOR EACH ROW EXECUTE FUNCTION aeon_review_run_immutable();
