-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-290: explicit, insert-only historical captures. Never rewrite tags.
SET LOCAL lock_timeout = '5s';

CREATE TABLE release_manifest_note_snapshots (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    project_node_id uuid NOT NULL,
    version text NOT NULL,
    snapshot jsonb NOT NULL CHECK ((
        jsonb_typeof(snapshot) = 'object'
        AND snapshot->>'schema' = 'aeon.release-note-snapshot.v1'
        AND snapshot->>'membership_source' = 'release-manifest-tickets'
        AND snapshot->>'label' = 'backfilled'
        AND snapshot->>'backfilled' = 'true'
        AND snapshot->>'tenant_id' = tenant_id::text
        AND snapshot->>'project_node_id' = project_node_id::text
        AND snapshot->>'version' = version) IS TRUE),
    PRIMARY KEY (tenant_id, project_node_id, version),
    FOREIGN KEY (tenant_id, project_node_id) REFERENCES nodes(tenant_id, id)
);
ALTER TABLE release_manifest_note_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE release_manifest_note_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY release_manifest_note_snapshots_tenant ON release_manifest_note_snapshots
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY release_manifest_note_snapshots_project_visibility ON release_manifest_note_snapshots AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    WITH CHECK ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]));
CREATE TRIGGER release_manifest_note_snapshots_immutable
    BEFORE UPDATE OR DELETE ON release_manifest_note_snapshots
    FOR EACH ROW EXECUTE FUNCTION aeon_release_note_snapshots_immutable();
