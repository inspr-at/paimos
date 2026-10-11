-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1085 / S05 / M2: new receipt tables only. Existing writers, definitions
-- and queue rows acquire no mandatory fields or launch authority.
SET LOCAL lock_timeout = '5s';

CREATE TABLE routine_runs (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 recurrence_id uuid NOT NULL,
 occurrence_key text NOT NULL CHECK (octet_length(occurrence_key) BETWEEN 1 AND 256),
 definition_revision bigint NOT NULL CHECK (definition_revision>0),
 scope_type text NOT NULL CHECK (scope_type IN ('project','personal','workspace')),
 scope_project_id uuid,
 owner_principal_id uuid NOT NULL,
 output_project_id uuid NOT NULL,
 output_parent_id uuid NOT NULL,
 assignment jsonb NOT NULL CHECK (jsonb_typeof(assignment)='object' AND octet_length(assignment::text)<=131072),
 assignment_digest text NOT NULL CHECK (assignment_digest ~ '^[a-f0-9]{64}$'),
 policy_snapshot jsonb NOT NULL CHECK (jsonb_typeof(policy_snapshot)='object' AND octet_length(policy_snapshot::text)<=262144),
 policy_digest text NOT NULL CHECK (policy_digest ~ '^[a-f0-9]{64}$'),
 execute_consent boolean NOT NULL DEFAULT false,
 consent_revision bigint NOT NULL DEFAULT 0,
 execution_principal_id uuid,
 work_node_id uuid NOT NULL,
 work_order_id uuid,
 agent_run_id uuid,
 source_event_id bigint,
 source_receipt jsonb NOT NULL CHECK (jsonb_typeof(source_receipt)='object' AND octet_length(source_receipt::text)<=16384),
 state text NOT NULL DEFAULT 'pending' CHECK (octet_length(state) BETWEEN 1 AND 64),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,id),
 UNIQUE (tenant_id,recurrence_id,occurrence_key),
 FOREIGN KEY (tenant_id,recurrence_id) REFERENCES recurrence_definitions(tenant_id,recurrence_id),
 FOREIGN KEY (tenant_id,recurrence_id,occurrence_key) REFERENCES recurrence_occurrences(tenant_id,recurrence_id,occurrence_key) DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY (tenant_id,owner_principal_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,execution_principal_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,scope_project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,output_project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,output_parent_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,work_node_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,work_order_id) REFERENCES work_orders(tenant_id,node_id),
 FOREIGN KEY (tenant_id,work_order_id,agent_run_id) REFERENCES agent_runs(tenant_id,work_order_id,id),
 FOREIGN KEY (tenant_id,source_event_id) REFERENCES events(tenant_id,id),
 CHECK ((scope_type='project')=(scope_project_id IS NOT NULL)),
 CHECK ((work_order_id IS NULL)=(agent_run_id IS NULL))
);
ALTER TABLE routine_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE routine_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY routine_runs_tenant ON routine_runs
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY routine_runs_scope ON routine_runs AS RESTRICTIVE
 USING (EXISTS(SELECT 1 FROM recurrence_definitions d WHERE d.tenant_id=routine_runs.tenant_id AND d.recurrence_id=routine_runs.recurrence_id)
  AND aeon_routine_scope_visible(scope_type,scope_project_id,owner_principal_id)
  AND ((SELECT aeon_visible_all()) OR output_project_id=ANY((SELECT aeon_visible_projects())::uuid[])))
 WITH CHECK (EXISTS(SELECT 1 FROM recurrence_definitions d WHERE d.tenant_id=routine_runs.tenant_id AND d.recurrence_id=routine_runs.recurrence_id)
  AND aeon_routine_scope_visible(scope_type,scope_project_id,owner_principal_id)
  AND ((SELECT aeon_visible_all()) OR output_project_id=ANY((SELECT aeon_visible_projects())::uuid[])));
CREATE INDEX routine_runs_history ON routine_runs(tenant_id,recurrence_id,created_at DESC,id DESC);
CREATE INDEX routine_runs_owner_history ON routine_runs(tenant_id,owner_principal_id,created_at DESC,id DESC);
CREATE INDEX routine_runs_pending ON routine_runs(tenant_id,created_at,id) WHERE state='pending';

-- Pending run identity/digests in occurrence audit snapshots share the run's
-- personal/workspace/project boundary. Legacy snapshots without run stay valid.
CREATE POLICY events_routine_run_scope ON events AS RESTRICTIVE FOR SELECT
 USING (type NOT IN ('recurrence.occurred','recurrence.skipped')
  OR ((after->'run' IS NULL OR after->'run'='null'::jsonb
       OR EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=events.tenant_id AND r.id=aeon_uuid_or_null(after#>>'{run,id}')))
   AND (before->'run' IS NULL OR before->'run'='null'::jsonb
       OR EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=events.tenant_id AND r.id=aeon_uuid_or_null(before#>>'{run,id}')))));

CREATE TABLE routine_attempts (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 run_id uuid NOT NULL,
 attempt_key text NOT NULL CHECK (octet_length(attempt_key) BETWEEN 1 AND 128),
 role text NOT NULL CHECK (octet_length(role) BETWEEN 1 AND 64),
 agent_run_id uuid,
 principal_id uuid,
 assignment_digest text NOT NULL CHECK (assignment_digest ~ '^[a-f0-9]{64}$'),
 state text NOT NULL DEFAULT 'pending' CHECK (octet_length(state) BETWEEN 1 AND 64),
 process_receipt jsonb CHECK (process_receipt IS NULL OR (jsonb_typeof(process_receipt)='object' AND octet_length(process_receipt::text)<=65536)),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,id),
 UNIQUE (tenant_id,run_id,id),
 UNIQUE (tenant_id,run_id,attempt_key),
 FOREIGN KEY (tenant_id,run_id) REFERENCES routine_runs(tenant_id,id),
 FOREIGN KEY (tenant_id,agent_run_id) REFERENCES agent_runs(tenant_id,id),
 FOREIGN KEY (tenant_id,principal_id) REFERENCES principals(tenant_id,id)
);
ALTER TABLE routine_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE routine_attempts FORCE ROW LEVEL SECURITY;
CREATE POLICY routine_attempts_tenant ON routine_attempts
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY routine_attempts_scope ON routine_attempts AS RESTRICTIVE
 USING (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_attempts.tenant_id AND r.id=routine_attempts.run_id))
 WITH CHECK (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_attempts.tenant_id AND r.id=routine_attempts.run_id));
CREATE INDEX routine_attempts_history ON routine_attempts(tenant_id,run_id,created_at DESC,id DESC);

CREATE TABLE routine_actions (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 run_id uuid NOT NULL,
 attempt_id uuid,
 action_key text NOT NULL CHECK (octet_length(action_key) BETWEEN 1 AND 128),
 kind text NOT NULL CHECK (kind IN ('work.create','work.update','knowledge.write','pr.open','pipeline.request')),
 request_digest text NOT NULL CHECK (request_digest ~ '^[a-f0-9]{64}$'),
 target_node_id uuid,
 target_revision bigint,
 policy_digest text CHECK (policy_digest IS NULL OR policy_digest ~ '^[a-f0-9]{64}$'),
 evaluation_id uuid,
 approval_id uuid,
 state text NOT NULL DEFAULT 'pending' CHECK (octet_length(state) BETWEEN 1 AND 64),
 result jsonb CHECK (result IS NULL OR (jsonb_typeof(result)='object' AND octet_length(result::text)<=65536)),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,id),
 UNIQUE (tenant_id,run_id,id),
 UNIQUE (tenant_id,run_id,action_key),
 FOREIGN KEY (tenant_id,run_id) REFERENCES routine_runs(tenant_id,id),
 FOREIGN KEY (tenant_id,run_id,attempt_id) REFERENCES routine_attempts(tenant_id,run_id,id),
 FOREIGN KEY (tenant_id,target_node_id) REFERENCES nodes(tenant_id,id)
);
ALTER TABLE routine_actions ENABLE ROW LEVEL SECURITY;
ALTER TABLE routine_actions FORCE ROW LEVEL SECURITY;
CREATE POLICY routine_actions_tenant ON routine_actions
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY routine_actions_scope ON routine_actions AS RESTRICTIVE
 USING (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_actions.tenant_id AND r.id=routine_actions.run_id))
 WITH CHECK (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_actions.tenant_id AND r.id=routine_actions.run_id));
CREATE INDEX routine_actions_history ON routine_actions(tenant_id,run_id,created_at DESC,id DESC);

CREATE TABLE routine_effect_outbox (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 run_id uuid NOT NULL,
 action_id uuid,
 effect_key text NOT NULL CHECK (octet_length(effect_key) BETWEEN 1 AND 128),
 kind text NOT NULL CHECK (octet_length(kind) BETWEEN 1 AND 64),
 payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object' AND octet_length(payload::text)<=65536),
 state text NOT NULL DEFAULT 'pending' CHECK (octet_length(state) BETWEEN 1 AND 64),
 result jsonb CHECK (result IS NULL OR (jsonb_typeof(result)='object' AND octet_length(result::text)<=65536)),
 reconcile_after timestamptz NOT NULL DEFAULT clock_timestamp(),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,id),
 UNIQUE (tenant_id,run_id,effect_key),
 FOREIGN KEY (tenant_id,run_id) REFERENCES routine_runs(tenant_id,id),
 FOREIGN KEY (tenant_id,run_id,action_id) REFERENCES routine_actions(tenant_id,run_id,id)
);
ALTER TABLE routine_effect_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE routine_effect_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY routine_effect_outbox_tenant ON routine_effect_outbox
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY routine_effect_outbox_scope ON routine_effect_outbox AS RESTRICTIVE
 USING (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_effect_outbox.tenant_id AND r.id=routine_effect_outbox.run_id))
 WITH CHECK (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_effect_outbox.tenant_id AND r.id=routine_effect_outbox.run_id));
CREATE INDEX routine_effect_outbox_pending ON routine_effect_outbox(tenant_id,reconcile_after,id) WHERE state='pending';
CREATE INDEX routine_effect_outbox_history ON routine_effect_outbox(tenant_id,run_id,created_at DESC,id DESC);
