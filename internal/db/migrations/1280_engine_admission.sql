-- SPDX-License-Identifier: AGPL-3.0-only
-- Expansion only: older binaries have no writers for either new table.
SET LOCAL lock_timeout = '5s';
CREATE TABLE engine_admission_settings (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 shadow_enabled boolean NOT NULL DEFAULT false,
 revision bigint NOT NULL DEFAULT 0,
 updated_by uuid NOT NULL,
 updated_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,project_id),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY(tenant_id,updated_by) REFERENCES principals(tenant_id,id)
);
CREATE TABLE engine_admission_decisions (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 actor_principal_id uuid NOT NULL,
 request_id text NOT NULL,
 project_id uuid NOT NULL,
 request_sha256 text NOT NULL,
 decision jsonb NOT NULL,
 evaluated_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,actor_principal_id,request_id),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY(tenant_id,actor_principal_id) REFERENCES principals(tenant_id,id)
);
CREATE INDEX engine_admission_decisions_project ON engine_admission_decisions(tenant_id,project_id,evaluated_at);
ALTER TABLE engine_admission_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE engine_admission_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY engine_admission_settings_tenant ON engine_admission_settings
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY engine_admission_settings_project ON engine_admission_settings AS RESTRICTIVE
 USING(project_id=ANY((SELECT aeon_visible_projects())::uuid[]) OR (SELECT aeon_visible_all()))
 WITH CHECK(project_id=ANY((SELECT aeon_visible_projects())::uuid[]) OR (SELECT aeon_visible_all()));
ALTER TABLE engine_admission_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE engine_admission_decisions FORCE ROW LEVEL SECURITY;
CREATE POLICY engine_admission_decisions_tenant ON engine_admission_decisions
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY engine_admission_decisions_project ON engine_admission_decisions AS RESTRICTIVE
 USING(project_id=ANY((SELECT aeon_visible_projects())::uuid[]) OR (SELECT aeon_visible_all()))
 WITH CHECK(project_id=ANY((SELECT aeon_visible_projects())::uuid[]) OR (SELECT aeon_visible_all()));
