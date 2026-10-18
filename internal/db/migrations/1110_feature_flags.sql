-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-429. Additive rollout controls; a missing row means OFF/inherit.
SET LOCAL lock_timeout = '5s';

CREATE TABLE features (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    key text NOT NULL CHECK (key ~ '^[a-z][a-z0-9_-]{0,63}$'),
    project_id uuid,
    enabled boolean DEFAULT false,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE NULLS NOT DISTINCT (tenant_id, key, project_id),
    FOREIGN KEY (tenant_id, project_id) REFERENCES nodes(tenant_id, id)
);
-- NULL enabled means inherit. Keep reset rows so their revision never returns
-- to zero and a stale admin cannot overwrite a later decision.
ALTER TABLE features ENABLE ROW LEVEL SECURITY;
ALTER TABLE features FORCE ROW LEVEL SECURITY;
CREATE POLICY features_tenant ON features
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY features_project_visibility ON features AS RESTRICTIVE
    USING (project_id IS NULL OR (SELECT aeon_visible_all()) OR project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    WITH CHECK (project_id IS NULL OR (SELECT aeon_visible_all()) OR project_id = ANY ((SELECT aeon_visible_projects())::uuid[]));
