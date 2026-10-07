-- SPDX-License-Identifier: AGPL-3.0-only
-- Expansion only: older writers never write this new settings table.
SET LOCAL lock_timeout = '5s';
CREATE TABLE cross_family_policies (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    project_id uuid,
    mode text NOT NULL,
    allowed_families text[] NOT NULL DEFAULT '{}',
    updated_by uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id),
    FOREIGN KEY (tenant_id,updated_by) REFERENCES principals(tenant_id,id),
    CHECK (mode IN ('off','other_family','allowlist')),
    CHECK (cardinality(allowed_families) <= 6),
    CHECK (array_position(allowed_families,NULL) IS NULL),
    CHECK (allowed_families <@ ARRAY['openai','anthropic','xai','cursor','google','local']::text[]),
    CHECK ((mode='allowlist') = (cardinality(allowed_families)>0))
);
CREATE UNIQUE INDEX cross_family_policies_scope ON cross_family_policies
    (tenant_id,(coalesce(project_id,'00000000-0000-0000-0000-000000000000'::uuid)));
ALTER TABLE cross_family_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE cross_family_policies FORCE ROW LEVEL SECURITY;
CREATE POLICY cross_family_policies_tenant ON cross_family_policies
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid
        AND (project_id IS NULL OR (SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[])))
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid
        AND (project_id IS NULL OR (SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[])));
