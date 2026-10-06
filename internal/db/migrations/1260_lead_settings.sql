-- SPDX-License-Identifier: AGPL-3.0-only
-- Expansion only: old binaries neither read nor write this new policy table.
SET LOCAL lock_timeout = '5s';
CREATE TABLE project_lead_settings (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    project_id uuid,
    owner_person_id uuid,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    overrides jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_by uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id),
    FOREIGN KEY (tenant_id,owner_person_id) REFERENCES principals(tenant_id,id),
    FOREIGN KEY (tenant_id,updated_by) REFERENCES principals(tenant_id,id),
    CHECK (jsonb_typeof(overrides) = 'object'),
    CHECK ((project_id IS NULL) = (owner_person_id IS NULL))
);
CREATE UNIQUE INDEX project_lead_settings_scope ON project_lead_settings
    (tenant_id,(coalesce(project_id,'00000000-0000-0000-0000-000000000000'::uuid)));
ALTER TABLE project_lead_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_lead_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY project_lead_settings_tenant ON project_lead_settings
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid
        AND (project_id IS NULL OR (SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[])))
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid
        AND (project_id IS NULL OR (SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[])));
