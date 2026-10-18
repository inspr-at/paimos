-- SPDX-License-Identifier: AGPL-3.0-only
-- Expansion: old runs remain readable; only revision-bound new runs admit.
SET LOCAL lock_timeout = '5s';
CREATE TABLE delivery_admission_bindings (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 repository text NOT NULL,
 pull_request bigint NOT NULL,
 project_id uuid,
 ticket_node_id uuid,
 target_branch text NOT NULL,
 base_sha text NOT NULL,
 head_sha text NOT NULL,
 scope text NOT NULL,
 revision bigint NOT NULL DEFAULT 1,
 control_revision bigint NOT NULL DEFAULT 0,
 holds jsonb NOT NULL DEFAULT '[]',
 fix boolean NOT NULL DEFAULT false,
 PRIMARY KEY(tenant_id,repository,pull_request)
);
CREATE TABLE delivery_admission_approvals (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 repository text NOT NULL,
 pull_request bigint NOT NULL,
 binding_revision bigint NOT NULL,
 target_branch text NOT NULL,
 base_sha text NOT NULL,
 head_sha text NOT NULL,
 scope text NOT NULL,
 review_run_id uuid,
 visual_run_id uuid,
 PRIMARY KEY(tenant_id,repository,pull_request,binding_revision),
 FOREIGN KEY(tenant_id,review_run_id) REFERENCES review_runs(tenant_id,id),
 FOREIGN KEY(tenant_id,visual_run_id) REFERENCES review_runs(tenant_id,id)
);
CREATE TABLE delivery_admission_settings (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 block_on_nightly_red boolean NOT NULL DEFAULT true,
 nightly_state text NOT NULL DEFAULT 'unknown',
 holds jsonb NOT NULL DEFAULT '[]',
 PRIMARY KEY(tenant_id,project_id)
);
ALTER TABLE review_runs ADD COLUMN admission_revision bigint;
ALTER TABLE delivery_admission_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_admission_bindings FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_admission_bindings_tenant ON delivery_admission_bindings
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE delivery_admission_approvals ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_admission_approvals FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_admission_approvals_tenant ON delivery_admission_approvals
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE delivery_admission_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_admission_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_admission_settings_tenant ON delivery_admission_settings
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
