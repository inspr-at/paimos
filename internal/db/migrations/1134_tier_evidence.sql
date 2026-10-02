-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
ALTER TABLE harness_session_usage ADD COLUMN model_time_ms bigint
 CHECK (model_time_ms BETWEEN 0 AND 1000000000000);
-- Narrow the last-run candidate lookup to this exact session identity.
CREATE INDEX harness_sessions_tier_sample ON harness_sessions
 (tenant_id,project_id,agent_principal_id,harness,model,reasoning_effort)
 WHERE run_id IS NOT NULL;
CREATE TABLE harness_tier_history (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id bigint GENERATED ALWAYS AS IDENTITY,
 session_id uuid NOT NULL,
 action text NOT NULL CHECK (action IN ('requested','switch_requested','approved','declined','changed','cancelled','undo_requested','undone','rejected')),
 from_tier text CHECK (from_tier IN ('default','fast','fastest')),
 to_tier text NOT NULL CHECK (to_tier IN ('default','fast','fastest')),
 actor_id uuid NOT NULL,
 request_id uuid,
 control_id uuid,
 undo_of_control_id uuid,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,id),
 FOREIGN KEY (tenant_id,session_id) REFERENCES harness_sessions(tenant_id,id),
 FOREIGN KEY (tenant_id,actor_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,request_id) REFERENCES harness_tier_requests(tenant_id,id),
 FOREIGN KEY (tenant_id,control_id) REFERENCES harness_controls(tenant_id,id),
 FOREIGN KEY (tenant_id,undo_of_control_id) REFERENCES harness_controls(tenant_id,id)
);
CREATE INDEX harness_tier_history_session ON harness_tier_history(tenant_id,session_id,id DESC);
ALTER TABLE harness_tier_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_tier_history FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_tier_history_tenant ON harness_tier_history
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
