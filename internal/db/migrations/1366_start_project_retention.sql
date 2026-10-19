-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1478. Expansion: source is separate from editable node fields. No FK
-- to nodes: the minimal receipt survives permanent project deletion.
SET LOCAL lock_timeout = '5s';
CREATE TABLE import_project_retention (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 source text NOT NULL CHECK (source = 'start'),
 imported_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 engaged_at timestamptz,
 engaged_by uuid,
 last_checked_at timestamptz,
 deleted_at timestamptz,
 PRIMARY KEY (tenant_id, project_id)
);
CREATE INDEX import_project_retention_due ON import_project_retention
 (tenant_id, last_checked_at NULLS FIRST, imported_at, project_id)
 WHERE source='start' AND engaged_at IS NULL AND deleted_at IS NULL;
ALTER TABLE import_project_retention ENABLE ROW LEVEL SECURITY;
ALTER TABLE import_project_retention FORCE ROW LEVEL SECURITY;
CREATE POLICY import_project_retention_tenant ON import_project_retention
 USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
 WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY import_project_retention_visibility ON import_project_retention AS RESTRICTIVE
 USING ((aeon_visible_all() OR project_id=ANY(aeon_visible_projects()))) WITH CHECK ((aeon_visible_all() OR project_id=ANY(aeon_visible_projects())));

CREATE TABLE import_retention_settings (
 tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
 enabled boolean NOT NULL,
 dry_run boolean NOT NULL DEFAULT true,
 revision bigint NOT NULL DEFAULT 1
);
ALTER TABLE import_retention_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE import_retention_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY import_retention_settings_tenant ON import_retention_settings
 USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
 WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

-- The worker backfills old imports in bounded batches from this positive
-- event provenance, never titles, keys or mutable node fields.
CREATE INDEX events_start_import_projects ON events(tenant_id,node_id)
 WHERE type='node.created' AND metadata->>'imported_from'='start';

-- Candidate checks read immutable event evidence under the tenant/tree fence;
-- they do not write engagement from an event trigger after the event counter.
CREATE INDEX events_start_retention_refs ON events USING gin(node_refs)
 WHERE metadata->>'imported_from' IS DISTINCT FROM 'start';
