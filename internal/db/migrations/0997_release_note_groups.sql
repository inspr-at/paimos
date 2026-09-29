-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-372: freeze classification alongside the existing public note fields.
-- Existing immutable snapshots are deliberately not rewritten.
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION aeon_release_note_group(p_fields jsonb) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE
        WHEN lower(trim(coalesce(p_fields->>'type', ''))) = 'bug'
          OR EXISTS (SELECT 1 FROM jsonb_array_elements(
              CASE WHEN jsonb_typeof(p_fields->'tags') = 'array' THEN p_fields->'tags' ELSE '[]'::jsonb END
            ) tag WHERE lower(trim(CASE WHEN jsonb_typeof(tag) = 'string' THEN tag #>> '{}' ELSE tag->>'name' END)) = 'bug')
        THEN 'fixes'
        WHEN trim(coalesce(p_fields->>'pill_en','') || coalesce(p_fields->>'pill_de','') ||
                  coalesce(p_fields->>'benefit_en','') || coalesce(p_fields->>'benefit_de','')) <> ''
        THEN 'features'
        ELSE 'other'
    END;
$$;

CREATE OR REPLACE FUNCTION aeon_release_note_snapshot(p_project uuid, p_release uuid) RETURNS jsonb
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
            'group', aeon_release_note_group(n.fields),
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
