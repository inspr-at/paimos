-- SPDX-License-Identifier: AGPL-3.0-only
-- Record recovery is separate from process termination. Keep immutable audit
-- history and the old private digests as generation-revocation tombstones.
ALTER TABLE harness_sessions
    ADD COLUMN archived_at timestamptz,
    ADD COLUMN recovery_process_state text CHECK (recovery_process_state = 'unknown'),
    ADD COLUMN recovery_request_id uuid,
    ADD COLUMN recovery_request_digest bytea,
    ADD COLUMN recovery_actor_id uuid,
    ADD COLUMN recovery_reason text,
    ADD CONSTRAINT harness_recovery_actor FOREIGN KEY (tenant_id,recovery_actor_id) REFERENCES principals(tenant_id,id),
    ADD CONSTRAINT harness_recovery_closed CHECK (archived_at IS NULL OR (stopped_at IS NOT NULL AND recovery_process_state = 'unknown' AND recovery_request_id IS NOT NULL AND recovery_request_digest IS NOT NULL AND recovery_actor_id IS NOT NULL AND recovery_reason IS NOT NULL));
CREATE UNIQUE INDEX harness_recovery_request ON harness_sessions(tenant_id,recovery_request_id) WHERE recovery_request_id IS NOT NULL;
CREATE INDEX harness_revoked_generation ON harness_sessions(tenant_id,project_id,agent_principal_id) WHERE archived_at IS NOT NULL;

ALTER TABLE harness_sessions ADD COLUMN process_ownership jsonb, ADD COLUMN process_observed_at timestamptz;
ALTER TABLE harness_controls DROP CONSTRAINT harness_controls_kind_check;
ALTER TABLE harness_controls ADD CONSTRAINT harness_controls_kind_check CHECK (kind IN ('interrupt','stop','force_stop'));
ALTER TABLE harness_controls ADD COLUMN expected_ownership jsonb, ADD COLUMN request_digest bytea, ADD COLUMN expires_at timestamptz;
ALTER TABLE harness_controls ADD CONSTRAINT harness_force_identity CHECK (kind <> 'force_stop' OR (expected_ownership IS NOT NULL AND request_digest IS NOT NULL AND expires_at IS NOT NULL));
