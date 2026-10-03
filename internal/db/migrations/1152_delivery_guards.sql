-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-596 P1. Guards only read and refuse; all maintenance belongs to stores.
-- No new advisory/row locks: P2/P3 writers must use AEON-586's global order.
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION aeon_guard_project_release() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM nodes n JOIN node_kinds k
        ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
        WHERE n.tenant_id = NEW.tenant_id AND n.id = NEW.release_node_id
          AND n.project_id = NEW.project_node_id AND n.deleted_at IS NULL AND k.slug = 'release') THEN
        RAISE EXCEPTION 'project release requires a live release node in its project';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF (NEW.tenant_id, NEW.project_node_id, NEW.release_node_id, NEW.origin)
            IS DISTINCT FROM (OLD.tenant_id, OLD.project_node_id, OLD.release_node_id, OLD.origin) THEN
            RAISE EXCEPTION 'project release identity and origin are immutable';
        END IF;
        IF NEW.state IS DISTINCT FROM OLD.state AND NOT (
            (OLD.state = 'planned' AND NEW.state IN ('building', 'frozen', 'abandoned'))
            OR (OLD.state = 'building' AND NEW.state IN ('planned', 'frozen', 'abandoned'))
            OR (OLD.state = 'frozen' AND NEW.state IN ('released', 'abandoned'))
            OR (OLD.state = 'frozen' AND NEW.state = 'building' AND OLD.cut_at IS NULL)) THEN
            RAISE EXCEPTION 'invalid project release state transition';
        END IF;
        IF NEW.visibility IS DISTINCT FROM OLD.visibility AND NOT (
            OLD.visibility = 'internal' AND NEW.visibility = 'published'
            AND OLD.state = 'planned' AND NEW.state = 'planned') THEN
            RAISE EXCEPTION 'only a planned internal release may become published';
        END IF;
        IF NEW.sequence IS DISTINCT FROM OLD.sequence AND NOT (
            OLD.visibility = 'internal' AND NEW.visibility = 'published'
            AND OLD.state = 'planned' AND NEW.state = 'planned') THEN
            RAISE EXCEPTION 'published release sequence is immutable';
        END IF;
        IF NEW.entry_closes_at IS DISTINCT FROM OLD.entry_closes_at
            AND (OLD.state <> 'planned' OR NEW.state <> 'planned') THEN
            RAISE EXCEPTION 'entry deadline may change only while planned';
        END IF;
        IF OLD.version IS NOT NULL AND (NEW.version, NEW.version_scheme, NEW.cut_at)
            IS DISTINCT FROM (OLD.version, OLD.version_scheme, OLD.cut_at) THEN
            RAISE EXCEPTION 'cut version and scheme are immutable';
        END IF;
        IF OLD.version IS NULL AND NEW.version IS NOT NULL AND (OLD.state <> 'frozen' OR NEW.state <> 'frozen') THEN
            RAISE EXCEPTION 'only a frozen release may be cut';
        END IF;
        IF OLD.included_in_release_id IS NOT NULL AND NEW.included_in_release_id IS DISTINCT FROM OLD.included_in_release_id THEN
            RAISE EXCEPTION 'internal release inclusion is immutable';
        END IF;
    END IF;
    IF NEW.included_in_release_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM project_releases r WHERE r.tenant_id = NEW.tenant_id
          AND r.project_node_id = NEW.project_node_id AND r.release_node_id = NEW.included_in_release_id
          AND r.visibility = 'published' AND r.state = 'released') THEN
        RAISE EXCEPTION 'internal release inclusion requires a published release in its project';
    END IF;
    -- All states count, including abandoned numbers. The C-collated rank and
    -- numbered order cannot diverge. Stores serialize writers at the fence.
    IF NEW.visibility = 'published' AND NEW.sequence > 0
        AND NEW.rank ~ '^[0-9A-Za-z]{0,31}[1-9A-Za-z]$' AND EXISTS (
        SELECT 1 FROM project_releases r WHERE r.tenant_id = NEW.tenant_id
          AND r.project_node_id = NEW.project_node_id AND r.visibility = 'published'
          AND r.release_node_id <> NEW.release_node_id
          AND ((r.sequence < NEW.sequence AND r.rank > NEW.rank)
            OR (r.sequence > NEW.sequence AND r.rank < NEW.rank))) THEN
        RAISE EXCEPTION 'published releases keep their numbered order';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER project_releases_guard BEFORE INSERT OR UPDATE ON project_releases
    FOR EACH ROW EXECUTE FUNCTION aeon_guard_project_release();

CREATE FUNCTION aeon_guard_ships_in() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM nodes n JOIN project_delivery d
        ON d.tenant_id = n.tenant_id AND d.project_node_id = n.project_id
        WHERE n.tenant_id = NEW.tenant_id AND n.id = NEW.item_node_id AND n.project_id = NEW.project_node_id) THEN
        RAISE EXCEPTION 'ships_in item must belong to its adopted project';
    END IF;
    -- Admission is separate from maintenance. UPDATE OF fires even if the
    -- caller assigns the same item id; ordinary rollover never assigns it.
    IF TG_OP = 'INSERT' OR TG_ARGV[0] = 'admission' THEN
        IF NOT EXISTS (SELECT 1 FROM nodes n JOIN node_kinds k
            ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
            WHERE n.tenant_id = NEW.tenant_id AND n.id = NEW.item_node_id
              AND k.slug IN ('epic', 'ticket', 'task')
              AND (n.deleted_at IS NULL OR NEW.source = 'adopted')) THEN
            RAISE EXCEPTION 'ships_in admission requires a live epic, ticket or task (adopted tombstones allowed)';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER ships_in_guard BEFORE INSERT OR UPDATE ON ships_in
    FOR EACH ROW EXECUTE FUNCTION aeon_guard_ships_in();
CREATE TRIGGER ships_in_item_guard BEFORE UPDATE OF item_node_id ON ships_in
    FOR EACH ROW EXECUTE FUNCTION aeon_guard_ships_in('admission');

CREATE FUNCTION aeon_guard_delivery_node_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM project_releases r WHERE r.tenant_id = OLD.tenant_id AND r.release_node_id = OLD.id
        AND (NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.kind_id IS DISTINCT FROM OLD.kind_id
            OR (NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL AND r.state <> 'abandoned'))) THEN
        RAISE EXCEPTION 'release node cannot move, change kind or be deleted before abandon';
    END IF;
    IF EXISTS (SELECT 1 FROM ships_in s WHERE s.tenant_id = OLD.tenant_id AND s.item_node_id = OLD.id
        AND (NEW.project_id IS DISTINCT FROM OLD.project_id
            OR (s.release_node_id IS NOT NULL AND NEW.kind_id IS DISTINCT FROM OLD.kind_id
                AND NOT EXISTS (SELECT 1 FROM node_kinds k WHERE k.tenant_id = NEW.tenant_id
                    AND k.id = NEW.kind_id AND k.slug IN ('epic', 'ticket', 'task'))))) THEN
        RAISE EXCEPTION 'placed item cannot leave its project or change to an unshippable kind in a release';
    END IF;
    RETURN NULL;
END;
$$;
-- No column list: project-root BEFORE triggers and descendant cascades count.
CREATE TRIGGER nodes_delivery_identity_guard AFTER UPDATE ON nodes
    FOR EACH ROW WHEN (OLD.project_id IS DISTINCT FROM NEW.project_id
        OR OLD.kind_id IS DISTINCT FROM NEW.kind_id OR OLD.deleted_at IS DISTINCT FROM NEW.deleted_at)
    EXECUTE FUNCTION aeon_guard_delivery_node_identity();

CREATE TRIGGER project_release_note_snapshots_immutable BEFORE UPDATE OR DELETE ON project_release_note_snapshots
    FOR EACH ROW EXECUTE FUNCTION aeon_release_note_snapshots_immutable();
CREATE TRIGGER project_release_note_snapshots_released AFTER INSERT ON project_release_note_snapshots
    FOR EACH ROW EXECUTE FUNCTION aeon_record_released_snapshot();
