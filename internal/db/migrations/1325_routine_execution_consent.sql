-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1084 / S04 / M6: default-off consent; no qualification on installation.
SET LOCAL lock_timeout = '5s';

CREATE TABLE routine_execution_qualifications (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 project_id uuid NOT NULL,
 owner_person_id uuid NOT NULL,
 policy_digest text NOT NULL,
 server_digest text NOT NULL,
 daemon_digest text NOT NULL,
 capability_digest text NOT NULL,
 host_mapping_digest text NOT NULL,
 budget_modes jsonb NOT NULL,
 coordinator_acceptance text NOT NULL,
 ops_attestation text NOT NULL,
 recorded_by uuid NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 revoked_at timestamptz,
 PRIMARY KEY (tenant_id,id),
 FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,owner_person_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,recorded_by) REFERENCES principals(tenant_id,id)
);
ALTER TABLE routine_execution_qualifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE routine_execution_qualifications FORCE ROW LEVEL SECURITY;
CREATE POLICY routine_execution_qualifications_tenant ON routine_execution_qualifications
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY routine_execution_qualifications_project ON routine_execution_qualifications AS RESTRICTIVE
 USING ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));

CREATE TABLE project_routine_execution_settings (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 revision bigint NOT NULL DEFAULT 1,
 automatic_launch_enabled boolean NOT NULL DEFAULT false,
 qualification_id uuid,
 updated_by uuid NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,project_id),
 FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,updated_by) REFERENCES principals(tenant_id,id)
);
ALTER TABLE project_routine_execution_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_routine_execution_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY project_routine_execution_settings_tenant ON project_routine_execution_settings
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY project_routine_execution_settings_project ON project_routine_execution_settings AS RESTRICTIVE
 USING ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));

-- Opaque bindings have no foreign key to later execution/evidence slices.
ALTER TABLE recurrence_definitions ADD COLUMN consent_policy_digest text;
ALTER TABLE recurrence_definitions ADD COLUMN consent_qualification_id uuid;
