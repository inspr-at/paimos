-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1131 / S11 / M5. Existing writers acquire only a nullable linkage.
-- Qualification and budget UUIDs are opaque pins owned by S04/S09; M6 follows
-- this expansion, so no forward foreign key or inferred qualification exists.
SET LOCAL lock_timeout = '5s';

ALTER TABLE agent_runs ADD COLUMN routine_launch_id uuid;
CREATE INDEX routine_runs_agent_run ON routine_runs(tenant_id,agent_run_id) WHERE agent_run_id IS NOT NULL;

CREATE TABLE routine_launch_intents (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL,
 routine_run_id uuid NOT NULL,
 agent_run_id uuid,
 project_id uuid NOT NULL,
 owner_principal_id uuid NOT NULL,
 worker_principal_id uuid NOT NULL,
 kind text NOT NULL DEFAULT 'worker',
 lead_generation bigint,
 binding jsonb NOT NULL,
 state text NOT NULL DEFAULT 'pending',
 worktree_id text,
 claimed_at timestamptz,
 exit_observed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,id),
 UNIQUE (tenant_id,agent_run_id),
 FOREIGN KEY (tenant_id,routine_run_id) REFERENCES routine_runs(tenant_id,id),
 FOREIGN KEY (tenant_id,agent_run_id) REFERENCES agent_runs(tenant_id,id),
 FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,owner_principal_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,worker_principal_id) REFERENCES principals(tenant_id,id)
);
ALTER TABLE routine_launch_intents ENABLE ROW LEVEL SECURITY;
ALTER TABLE routine_launch_intents FORCE ROW LEVEL SECURITY;
CREATE POLICY routine_launch_intents_tenant ON routine_launch_intents
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY routine_launch_intents_scope ON routine_launch_intents AS RESTRICTIVE
 USING (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_launch_intents.tenant_id AND r.id=routine_launch_intents.routine_run_id))
 WITH CHECK (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_launch_intents.tenant_id AND r.id=routine_launch_intents.routine_run_id));
CREATE INDEX routine_launch_intents_pending ON routine_launch_intents(tenant_id,created_at,id) WHERE state='pending';

-- Append-only accepted evidence; process labels and heartbeat expiry cannot
-- clear a claim. Only a verified owned-exit report advances exit_observed_at.
CREATE TABLE routine_launch_evidence (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 launch_id uuid NOT NULL,
 kind text NOT NULL,
 receipt jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,launch_id,kind),
 FOREIGN KEY (tenant_id,launch_id) REFERENCES routine_launch_intents(tenant_id,id)
);
ALTER TABLE routine_launch_evidence ENABLE ROW LEVEL SECURITY;
ALTER TABLE routine_launch_evidence FORCE ROW LEVEL SECURITY;
CREATE POLICY routine_launch_evidence_tenant ON routine_launch_evidence
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY routine_launch_evidence_scope ON routine_launch_evidence AS RESTRICTIVE
 USING (EXISTS(SELECT 1 FROM routine_launch_intents l WHERE l.tenant_id=routine_launch_evidence.tenant_id AND l.id=routine_launch_evidence.launch_id))
 WITH CHECK (EXISTS(SELECT 1 FROM routine_launch_intents l WHERE l.tenant_id=routine_launch_evidence.tenant_id AND l.id=routine_launch_evidence.launch_id));
CREATE POLICY routine_launch_evidence_append ON routine_launch_evidence AS RESTRICTIVE FOR UPDATE USING (false);
CREATE POLICY routine_launch_evidence_retain ON routine_launch_evidence AS RESTRICTIVE FOR DELETE USING (false);
