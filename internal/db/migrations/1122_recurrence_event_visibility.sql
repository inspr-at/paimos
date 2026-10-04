-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-573: project readers must see recurrence history and retirement events.
-- Preserve tenant isolation, referenced-node checks and workspace-only domains.
SET LOCAL lock_timeout = '5s';
ALTER TABLE recurrences ADD COLUMN retired_at timestamptz;

-- A definition may carry workspace tags that its project readers cannot load.
-- Keep the complete audit snapshots, but omit only those tags from the reference
-- walk. The same UUID anywhere else in a snapshot still counts as a reference.
-- Every other event domain and every non-workspace tag uses the original walk.
CREATE FUNCTION aeon_recurrence_event_node_refs(p_tenant uuid, p_type text, p_before jsonb, p_after jsonb) RETURNS uuid[]
LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    prior text := current_setting('aeon.visible_projects', true);
    snapshots jsonb[] := ARRAY[p_before, p_after];
    tags jsonb;
    i integer;
BEGIN
    IF p_type NOT IN ('recurrence.created', 'recurrence.updated', 'recurrence.paused',
                     'recurrence.resumed', 'recurrence.deleted') THEN
        RETURN aeon_event_node_refs(p_tenant, p_type, p_before, p_after);
    END IF;
    -- The lookup stays tenant-scoped even while classifying hidden workspace tags.
    PERFORM set_config('aeon.visible_projects', '*', true);
    BEGIN
        FOR i IN 1..2 LOOP
            IF jsonb_typeof(snapshots[i] #> '{template,tags}') = 'array' THEN
                SELECT coalesce(jsonb_agg(tag.value ORDER BY tag.ordinality), '[]'::jsonb) INTO tags
                FROM jsonb_array_elements(snapshots[i] #> '{template,tags}') WITH ORDINALITY AS tag(value, ordinality)
                WHERE NOT EXISTS (
                    SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
                    WHERE n.tenant_id = p_tenant AND n.id = aeon_uuid_or_null(tag.value #>> '{}')
                      AND n.project_id IS NULL AND k.slug = 'tag');
                snapshots[i] := jsonb_set(snapshots[i], '{template,tags}', tags);
            END IF;
        END LOOP;
    EXCEPTION WHEN OTHERS THEN
        PERFORM set_config('aeon.visible_projects', coalesce(prior, ''), true);
        RAISE;
    END;
    PERFORM set_config('aeon.visible_projects', coalesce(prior, ''), true);
    RETURN aeon_event_node_refs(p_tenant, p_type, snapshots[1], snapshots[2]);
END;
$$;

CREATE OR REPLACE FUNCTION aeon_event_set_node_refs() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.node_refs := aeon_recurrence_event_node_refs(NEW.tenant_id, NEW.type, NEW.before, NEW.after);
    RETURN NEW;
END;
$$;

-- Copy existing tombstones onto the row under tenant RLS. Retirement reads never
-- depend on the caller's event visibility. Recompute only definition references;
-- event snapshots, actors, timestamps, IDs and occurrence receipts stay intact.
DO $$
DECLARE
    tenant uuid;
    prior_tenant text := current_setting('aeon.tenant_id', true);
    prior_visible text := current_setting('aeon.visible_projects', true);
BEGIN
    ALTER TABLE events DISABLE TRIGGER events_no_update_delete;
    CREATE POLICY events_recurrence_refs_backfill ON events FOR UPDATE
        USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
    FOR tenant IN SELECT id FROM tenants ORDER BY id LOOP
        PERFORM set_config('aeon.tenant_id', tenant::text, true);
        PERFORM set_config('aeon.visible_projects', '*', true);
        UPDATE recurrences r SET retired_at = e.retired_at, paused = true
        FROM (SELECT tenant_id, node_id, after->>'id' AS recurrence_id, min(at) AS retired_at
              FROM events WHERE tenant_id = tenant AND type = 'recurrence.deleted'
              GROUP BY tenant_id, node_id, after->>'id') e
        WHERE r.tenant_id = tenant AND r.tenant_id = e.tenant_id AND r.project_id = e.node_id
          AND r.id::text = e.recurrence_id AND r.retired_at IS NULL;
        UPDATE events e SET node_refs = computed.refs
        FROM (SELECT id, aeon_recurrence_event_node_refs(tenant_id, type, before, after) AS refs
              FROM events WHERE tenant_id = tenant
                AND type IN ('recurrence.created', 'recurrence.updated', 'recurrence.paused',
                             'recurrence.resumed', 'recurrence.deleted')) computed
        WHERE e.tenant_id = tenant AND e.id = computed.id AND e.node_refs IS DISTINCT FROM computed.refs;
    END LOOP;
    DROP POLICY events_recurrence_refs_backfill ON events;
    ALTER TABLE events ENABLE TRIGGER events_no_update_delete;
    PERFORM set_config('aeon.tenant_id', coalesce(prior_tenant, ''), true);
    PERFORM set_config('aeon.visible_projects', coalesce(prior_visible, ''), true);
END;
$$;

ALTER POLICY events_project_visibility ON events
    USING ((SELECT aeon_visible_all())
        OR (CASE
                WHEN node_id IS NULL THEN
                    (SELECT aeon_visibility_system())
                    OR actor_principal_id = ANY ((SELECT aeon_current_principals())::uuid[])
                ELSE EXISTS (SELECT 1 FROM nodes n WHERE n.tenant_id = events.tenant_id AND n.id = events.node_id)
                    AND (split_part(type, '.', 1) IN ('node', 'nodes', 'comment', 'comments', 'attachment',
                            'attachments', 'relation', 'relations', 'import', 'journey', 'intake', 'requirement',
                            'requirements', 'release', 'releases', 'knowledge', 'view', 'views', 'tag', 'tags',
                            'kind', 'kinds', 'profile', 'recurrence')
                         OR type IN ('status_autopilot.changed', 'status_autopilot.skipped')
                         OR actor_principal_id = ANY ((SELECT aeon_current_principals())::uuid[]))
            END
            AND (cardinality(node_refs) = 0
                 OR NOT EXISTS (SELECT 1 FROM unnest(node_refs) AS ref(id)
                                WHERE ref.id NOT IN (SELECT n.id FROM nodes n)))));
