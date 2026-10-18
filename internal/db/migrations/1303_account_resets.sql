-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1035: additive rollout. Rollback retains reports/actions and runs the
-- older binary; spent vendor credits must never be restored by a down migration.
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_accounts ADD COLUMN reset_report jsonb;
ALTER TABLE agent_accounts ADD COLUMN reset_policy_person_id uuid;
ALTER TABLE agent_accounts ADD COLUMN reset_policy_link_revision bigint;

CREATE TABLE account_reset_actions (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 account_id uuid,
 person_id uuid,
 binding_revision bigint,
 policy_revision bigint,
 state text,
 by_kind text,
 expected_count integer,
 expired_at timestamptz,
 requested_at timestamptz,
 completed_at timestamptz,
 undo_until timestamptz,
 raised_pace_points double precision,
 raised_pace_until timestamptz,
 result jsonb,
 PRIMARY KEY (tenant_id,id)
);
CREATE UNIQUE INDEX account_reset_actions_pending_idx ON account_reset_actions
 (tenant_id,account_id) WHERE state IN ('pending','unknown','undo_pending','undo_unknown');
CREATE INDEX account_reset_actions_pace_idx ON account_reset_actions
 (tenant_id,account_id,completed_at DESC) WHERE state='succeeded';
ALTER TABLE account_reset_actions ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_reset_actions FORCE ROW LEVEL SECURITY;
CREATE POLICY account_reset_actions_tenant ON account_reset_actions
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
