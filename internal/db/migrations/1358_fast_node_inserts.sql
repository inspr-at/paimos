-- SPDX-License-Identifier: AGPL-3.0-only
-- aeon:contract-phase AEON-1422 expanded-in=v261015121011.0.0 expansion-migration=1237_work_release_leaf_lifecycle.sql
-- AEON-1422: avoid repeated whole-tree work during ordinary bulk INSERTs.
-- Function-only replacement: no schema/data changes or writer contract change.
-- The guard requires pinned expansion evidence for CREATE OR REPLACE; both
-- original functions are present in this already published expansion release.
SET LOCAL lock_timeout = '5s';

CREATE OR REPLACE FUNCTION aeon_validate_node_tree() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    parent_kind_id uuid;
    parent_deleted_at timestamptz;
    allowed text[];
    child_slug text;
    check_link boolean;
BEGIN
    -- Serialize changes to one tenant's tree so concurrent moves cannot form a cycle.
    PERFORM pg_advisory_xact_lock(hashtextextended(NEW.tenant_id::text, 0));
    IF TG_OP = 'INSERT' THEN
        check_link := true;
    ELSE
        check_link := NEW.parent_id IS DISTINCT FROM OLD.parent_id
                   OR NEW.kind_id IS DISTINCT FROM OLD.kind_id;
    END IF;
    IF check_link AND NEW.parent_id IS NOT NULL THEN
        IF NEW.parent_id = NEW.id THEN
            RAISE EXCEPTION 'node cannot parent itself';
        END IF;
        SELECT p.kind_id, p.deleted_at INTO parent_kind_id, parent_deleted_at
        FROM nodes p WHERE p.tenant_id = NEW.tenant_id AND p.id = NEW.parent_id;
        IF NOT FOUND OR parent_deleted_at IS NOT NULL THEN
            RAISE EXCEPTION 'parent node does not exist or is deleted';
        END IF;
        SELECT k.allowed_child_kinds INTO allowed FROM node_kinds k
        WHERE k.tenant_id = NEW.tenant_id AND k.id = parent_kind_id;
        SELECT k.slug INTO child_slug FROM node_kinds k
        WHERE k.tenant_id = NEW.tenant_id AND k.id = NEW.kind_id;
        IF allowed IS NOT NULL AND NOT child_slug = ANY(allowed) THEN
            RAISE EXCEPTION 'child kind is not allowed under parent kind';
        END IF;
        IF EXISTS (
            WITH RECURSIVE ancestors(id, parent_id) AS (
                SELECT p.id, p.parent_id FROM nodes p
                WHERE p.tenant_id = NEW.tenant_id AND p.id = NEW.parent_id
                UNION ALL
                -- Keep each recursive edge a parameterized point lookup.
                -- A hash join can rescan the growing nodes table per INSERT
                -- when bulk writers have not refreshed statistics yet.
                SELECT p.id, p.parent_id FROM ancestors a
                JOIN LATERAL (
                    SELECT n.id, n.parent_id FROM nodes n
                    WHERE n.tenant_id = NEW.tenant_id AND n.id = a.parent_id
                    OFFSET 0
                ) p ON true
            )
            SELECT 1 FROM ancestors WHERE id = NEW.id
        ) THEN
            RAISE EXCEPTION 'node move would create a cycle';
        END IF;
    END IF;
    IF TG_OP = 'UPDATE' AND OLD.deleted_at IS NOT NULL
       AND NEW.deleted_at IS NULL AND NOT check_link
       AND NEW.parent_id IS NOT NULL THEN
        PERFORM 1 FROM nodes p WHERE p.tenant_id = NEW.tenant_id
            AND p.id = NEW.parent_id AND p.deleted_at IS NULL;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'cannot restore node under deleted parent';
        END IF;
    END IF;
    IF NEW.deleted_at IS NOT NULL AND (TG_OP = 'INSERT' OR OLD.deleted_at IS NULL) THEN
        IF EXISTS (SELECT 1 FROM nodes c WHERE c.tenant_id = NEW.tenant_id
                   AND c.parent_id = NEW.id AND c.deleted_at IS NULL) THEN
            RAISE EXCEPTION 'node has live children';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION aeon_work_release_inherit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 -- With neither membership nor retained parent intent visible in this
 -- tenant, there is nothing to remove, restore or inherit. Keep the full
 -- reconciliation path for every UPDATE and for tenants with placements,
 -- including explicit backlog and frozen/historical memberships. Invoker
 -- rights and the structural writer's existing tree fence remain unchanged.
 IF TG_OP='INSERT' AND NOT EXISTS (
  SELECT 1 FROM journey_tickets WHERE tenant_id=NEW.tenant_id
 ) AND NOT EXISTS (
  SELECT 1 FROM work_parent_releases WHERE tenant_id=NEW.tenant_id
 ) THEN RETURN NEW; END IF;
 IF TG_OP='UPDATE' THEN
  PERFORM aeon_work_reconcile_release_nodes(ARRAY[NEW.id,NEW.parent_id,OLD.parent_id]);
 ELSE
  PERFORM aeon_work_reconcile_release_nodes(ARRAY[NEW.id,NEW.parent_id]);
 END IF;
 RETURN NEW;
END;
$$;
