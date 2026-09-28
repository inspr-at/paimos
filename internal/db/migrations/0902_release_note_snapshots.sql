-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-256. Freeze pill and benefit text when a release is published.
-- A planning release stays a live preview: freezing at creation would lock an
-- empty membership before tickets are added. The deferred trigger runs at
-- commit, so tickets linked in the same transaction as a published insert are
-- part of the snapshot. A later edit of those tickets does not rewrite it.
-- Moving released to superseded does not capture again. A release already
-- published before this migration has no row: readers must not rebuild it
-- from ticket fields edited later.

CREATE TABLE journey_release_note_snapshots (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    release_node_id uuid NOT NULL,
    project_node_id uuid NOT NULL,
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object' AND snapshot->>'schema' = 'aeon.release-note-snapshot.v1'),
    PRIMARY KEY (tenant_id, release_node_id),
    FOREIGN KEY (tenant_id, project_node_id, release_node_id)
        REFERENCES journey_releases (tenant_id, project_node_id, release_node_id)
);
ALTER TABLE journey_release_note_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE journey_release_note_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY journey_release_note_snapshots_tenant ON journey_release_note_snapshots
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
-- Same restrictive project visibility as journey_releases (0821). A member of
-- one project cannot read another project's frozen notes by selecting this table.
CREATE POLICY journey_release_note_snapshots_project_visibility ON journey_release_note_snapshots AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    WITH CHECK ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]));

CREATE FUNCTION aeon_release_note_snapshots_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'release note snapshots are immutable';
END;
$$;
CREATE TRIGGER journey_release_note_snapshots_immutable
    BEFORE UPDATE OR DELETE ON journey_release_note_snapshots
    FOR EACH ROW EXECUTE FUNCTION aeon_release_note_snapshots_immutable();

-- One statement, so membership, revision and fields share one MVCC snapshot.
-- This is the live preview, and the capture at publication. A published
-- release that has no stored row must not be filled by calling it.
CREATE FUNCTION aeon_release_note_snapshot(p_project uuid, p_release uuid) RETURNS jsonb
LANGUAGE sql STABLE AS $$
    SELECT jsonb_build_object(
        'schema', 'aeon.release-note-snapshot.v1',
        'tenant_id', r.tenant_id,
        'project_node_id', r.project_node_id,
        'release_node_id', r.release_node_id,
        'version', coalesce(r.version, ''),
        'version_scheme', coalesce(r.version_scheme, ''),
        'release_revision', r.revision,
        'captured_at', statement_timestamp(),
        'membership_source', 'journey_tickets.release_node_id',
        'field_source', 'nodes.fields',
        'frozen', false,
        'tickets', coalesce((SELECT jsonb_agg(jsonb_build_object(
            'id', t.ticket_node_id,
            'key', coalesce(n.key, ''),
            'position', t.walker_position,
            'updated_at', n.updated_at,
            'fields', CASE
                WHEN n.id IS NOT NULL AND n.deleted_at IS NULL AND k.slug = 'ticket' THEN (
                    SELECT coalesce(jsonb_object_agg(f.key, f.value), '{}'::jsonb)
                    FROM jsonb_each(n.fields) f
                    WHERE f.key IN ('pill_en', 'pill_de', 'benefit_en', 'benefit_de', 'hide_from_release_notes'))
                WHEN n.id IS NOT NULL THEN jsonb_build_object('hide_from_release_notes', coalesce(n.fields->'hide_from_release_notes', 'false'::jsonb))
                ELSE NULL END,
            'unavailable', CASE
                WHEN n.id IS NULL THEN 'Member is unavailable.'
                WHEN n.deleted_at IS NOT NULL THEN 'Member was deleted before capture.'
                WHEN k.slug IS DISTINCT FROM 'ticket' THEN 'Member is not a ticket.'
                ELSE '' END)
            ORDER BY t.walker_position, t.ticket_node_id)
            FROM journey_tickets t
            LEFT JOIN nodes n ON n.tenant_id = t.tenant_id AND n.id = t.ticket_node_id AND n.project_id = t.project_node_id
            LEFT JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
            WHERE t.tenant_id = r.tenant_id AND t.project_node_id = r.project_node_id AND t.release_node_id = r.release_node_id), '[]'::jsonb))
    FROM journey_releases r
    JOIN nodes rn ON rn.tenant_id = r.tenant_id AND rn.id = r.release_node_id AND rn.deleted_at IS NULL
    JOIN nodes pn ON pn.tenant_id = r.tenant_id AND pn.id = r.project_node_id AND pn.deleted_at IS NULL
    WHERE r.tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid
      AND r.project_node_id = p_project
      AND r.release_node_id = p_release;
$$;

CREATE FUNCTION aeon_freeze_release_note_snapshot() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    snap jsonb;
BEGIN
    IF NEW.state NOT IN ('released', 'superseded') THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE' AND OLD.state IN ('released', 'superseded') THEN
        RETURN NEW;
    END IF;
    snap := aeon_release_note_snapshot(NEW.project_node_id, NEW.release_node_id);
    IF snap IS NULL THEN
        RAISE EXCEPTION 'release note snapshot could not be captured';
    END IF;
    INSERT INTO journey_release_note_snapshots (tenant_id, release_node_id, project_node_id, snapshot)
    VALUES (NEW.tenant_id, NEW.release_node_id, NEW.project_node_id, jsonb_set(snap, '{frozen}', 'true'::jsonb))
    ON CONFLICT (tenant_id, release_node_id) DO NOTHING;
    RETURN NEW;
END;
$$;

-- Deferred so membership written in the same transaction is visible, including
-- a release inserted already published and then linked before commit.
CREATE CONSTRAINT TRIGGER journey_releases_freeze_note_snapshot
    AFTER INSERT OR UPDATE OF state ON journey_releases
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION aeon_freeze_release_note_snapshot();
