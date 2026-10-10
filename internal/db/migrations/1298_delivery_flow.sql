-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1004 expansion: delivery flow runs (releases and changes), their steps
-- and incidents, read by project members. New tables only; older binaries
-- have no writers.
SET LOCAL lock_timeout = '5s';
CREATE TABLE delivery_flow_items (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('release','change')),
 ref text NOT NULL CHECK(length(ref) BETWEEN 1 AND 64),
 ticket_node_id uuid,
 title text NOT NULL CHECK(length(title) <= 300),
 prs bigint[] NOT NULL DEFAULT '{}' CHECK(cardinality(prs) <= 20),
 started_at timestamptz,
 ended_at timestamptz,
 target_minutes integer CHECK(target_minutes BETWEEN 1 AND 10080),
 target_from_step text,
 target_source text,
 gate_principal_id uuid,
 gate_what text CHECK(length(gate_what) <= 120),
 ops_eta_p50_at timestamptz,
 ops_eta_p90_at timestamptz,
 updated_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,id),
 UNIQUE(tenant_id,project_id,kind,ref),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY(tenant_id,ticket_node_id) REFERENCES nodes(tenant_id,id)
);
CREATE INDEX delivery_flow_items_window ON delivery_flow_items(tenant_id,project_id,started_at);
CREATE INDEX delivery_flow_items_open ON delivery_flow_items(tenant_id,project_id,id) WHERE ended_at IS NULL;
CREATE INDEX delivery_flow_items_ticket ON delivery_flow_items(tenant_id,ticket_node_id) WHERE ticket_node_id IS NOT NULL;

-- One row per step of a run. (source, source_key) is the reporter's identity,
-- so a replayed rollout, webhook or PAIMOS event converges on the same row.
CREATE TABLE delivery_flow_steps (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 id uuid NOT NULL,
 item_id uuid NOT NULL,
 source text NOT NULL CHECK(source IN ('github_app','paimos','ops_rollout')),
 source_key text NOT NULL CHECK(length(source_key) BETWEEN 1 AND 160),
 step_key text NOT NULL CHECK(step_key IN ('a','b','c','d','e','f','g','h','i','j','k','l','copy_gate','pin_gate','build','review','ci','queue','merge_round','hold','mitigation','switch','live_check')),
 round integer NOT NULL CHECK(round BETWEEN 1 AND 1000),
 kind text NOT NULL CHECK(kind IN ('work','wait','rework','recovery')),
 actor_type text NOT NULL CHECK(actor_type IN ('person','agent','ci','queue')),
 actor_principal_id uuid,
 actor_label text NOT NULL CHECK(length(actor_label) BETWEEN 1 AND 80),
 actor_model text CHECK(length(actor_model) <= 80),
 started_at timestamptz NOT NULL,
 ended_at timestamptz,
 outcome text CHECK(outcome IN ('ok','changes','red','flaky','degraded','green')),
 wait_reason text CHECK(wait_reason IN ('reviewer','queue','dependency','human_gate','rerun','release_train')),
 waits_for uuid,
 side boolean NOT NULL DEFAULT false,
 recorded_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,id),
 UNIQUE(tenant_id,item_id,source,source_key),
 CHECK(ended_at IS NULL OR ended_at >= started_at),
 FOREIGN KEY(tenant_id,item_id) REFERENCES delivery_flow_items(tenant_id,id),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id)
);
CREATE INDEX delivery_flow_steps_item ON delivery_flow_steps(tenant_id,item_id,started_at);
CREATE INDEX delivery_flow_steps_window ON delivery_flow_steps(tenant_id,project_id,started_at);
CREATE INDEX delivery_flow_steps_history ON delivery_flow_steps(tenant_id,project_id,step_key,ended_at) WHERE ended_at IS NOT NULL;

CREATE TABLE delivery_flow_incidents (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 id uuid NOT NULL,
 item_id uuid NOT NULL,
 source text NOT NULL CHECK(source IN ('github_app','paimos','ops_rollout')),
 source_key text NOT NULL CHECK(length(source_key) BETWEEN 1 AND 160),
 started_at timestamptz NOT NULL,
 ended_at timestamptz,
 severity text NOT NULL CHECK(severity IN ('degraded','down')),
 summary text NOT NULL CHECK(length(summary) BETWEEN 1 AND 300),
 recovery_step_ids uuid[] NOT NULL DEFAULT '{}' CHECK(cardinality(recovery_step_ids) <= 50),
 updated_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,id),
 UNIQUE(tenant_id,item_id,source,source_key),
 CHECK(ended_at IS NULL OR ended_at >= started_at),
 FOREIGN KEY(tenant_id,item_id) REFERENCES delivery_flow_items(tenant_id,id),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id)
);
CREATE INDEX delivery_flow_incidents_item ON delivery_flow_incidents(tenant_id,item_id,started_at);

-- Read: members of a visible project. The flow API also requires delivery.read.
ALTER TABLE delivery_flow_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_flow_items FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_flow_items_tenant ON delivery_flow_items
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_flow_items_project ON delivery_flow_items AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));
ALTER TABLE delivery_flow_steps ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_flow_steps FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_flow_steps_tenant ON delivery_flow_steps
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_flow_steps_project ON delivery_flow_steps AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));
ALTER TABLE delivery_flow_incidents ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_flow_incidents FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_flow_incidents_tenant ON delivery_flow_incidents
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_flow_incidents_project ON delivery_flow_incidents AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));
