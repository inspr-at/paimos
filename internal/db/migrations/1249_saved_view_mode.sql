-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-718: preserve mode, and retire only views without stored columns on
-- every tenant/instance. Deletion snapshots retain the same shape as the API.
SET LOCAL lock_timeout = '5s';
ALTER TABLE saved_views ADD COLUMN IF NOT EXISTS mode text NOT NULL DEFAULT 'list'
    CONSTRAINT saved_views_mode_check CHECK (mode IN ('list', 'outline', 'graph'));

DO $$
DECLARE
    target uuid;
    prior_tenant text := current_setting('aeon.tenant_id', true);
    prior_visible text := current_setting('aeon.visible_projects', true);
BEGIN
    FOR target IN SELECT id FROM tenants ORDER BY id LOOP
        PERFORM set_config('aeon.tenant_id', target::text, true);
        PERFORM set_config('aeon.visible_projects', '*', true);
        PERFORM 1 FROM tenants WHERE id = target FOR NO KEY UPDATE;
        -- Lock all affected records before updating them and before the first
        -- event counter lock. Already deleted rows never get another event.
        WITH legacy AS MATERIALIZED (
            SELECT v.*, to_jsonb(v) - 'tenant_id' - 'sort_field' - 'sort_direction'
                || jsonb_build_object('sort', jsonb_build_object(
                    'field', v.sort_field, 'direction', v.sort_direction)) AS snapshot
            FROM saved_views v
            WHERE v.tenant_id = target AND v.columns = '{}'::text[] AND v.deleted_at IS NULL
            ORDER BY v.id FOR NO KEY UPDATE
        ), retired AS (
            UPDATE saved_views v
            SET deleted_at = clock_timestamp(),
                updated_at = greatest(clock_timestamp(), v.updated_at + interval '1 microsecond')
            FROM legacy l
            WHERE v.tenant_id = target AND v.id = l.id
            RETURNING v.*, l.snapshot
        )
        INSERT INTO events (tenant_id, actor_principal_id, type, before, after)
        SELECT target, NULL, 'view.deleted', r.snapshot,
            to_jsonb(r) - 'tenant_id' - 'sort_field' - 'sort_direction' - 'snapshot'
                || jsonb_build_object('sort', jsonb_build_object(
                    'field', r.sort_field, 'direction', r.sort_direction))
        FROM retired r ORDER BY r.id;
    END LOOP;
    PERFORM set_config('aeon.tenant_id', coalesce(prior_tenant, ''), true);
    PERFORM set_config('aeon.visible_projects', coalesce(prior_visible, ''), true);
END;
$$;
