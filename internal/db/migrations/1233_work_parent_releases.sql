-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-653, reserved 1233. Placement is intent, never a release member:
-- only leaves enter journey_tickets. All columns are tenant/project metadata;
-- no principal identifiers or user content are stored in this table.
SET LOCAL lock_timeout = '5s';

CREATE TABLE work_parent_releases (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 parent_node_id uuid NOT NULL,
 project_node_id uuid NOT NULL,
 release_node_id uuid NOT NULL,
 PRIMARY KEY (tenant_id,parent_node_id),
 FOREIGN KEY (tenant_id,parent_node_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,project_node_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,release_node_id) REFERENCES nodes(tenant_id,id)
);
ALTER TABLE work_parent_releases ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_parent_releases FORCE ROW LEVEL SECURITY;
CREATE POLICY work_parent_releases_tenant ON work_parent_releases
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY work_parent_releases_project ON work_parent_releases AS RESTRICTIVE
 USING ((SELECT aeon_visible_all()) OR project_node_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK ((SELECT aeon_visible_all()) OR project_node_id=ANY((SELECT aeon_visible_projects())::uuid[]));

-- Only work edges count. A memory child containing work does not make the
-- root a parent, and a nested project starts a new release scope.
CREATE FUNCTION aeon_work_release_scope(roots uuid[])
RETURNS TABLE(root uuid,id uuid,kind_slug text,is_leaf boolean,project_id uuid)
LANGUAGE plpgsql STABLE AS $$
DECLARE root_ids uuid[]; node_ids uuid[];
BEGIN
 IF cardinality(roots)>100 THEN RAISE EXCEPTION 'release roots exceed 100' USING ERRCODE='54000'; END IF;
 WITH RECURSIVE walk(root,id,project_id) AS (
  SELECT n.id,n.id,n.project_id FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
   WHERE n.id=ANY(roots) AND n.deleted_at IS NULL AND k.slug IN ('work','ticket','task')
  UNION
  SELECT w.root,c.id,c.project_id FROM walk w JOIN nodes c ON c.parent_id=w.id AND c.project_id=w.project_id
   JOIN node_kinds k ON k.tenant_id=c.tenant_id AND k.id=c.kind_id
   WHERE c.deleted_at IS NULL AND k.slug IN ('work','ticket','task')
 ), bounded AS MATERIALIZED (SELECT w.root,w.id FROM walk w LIMIT 50001)
 SELECT array_agg(b.root),array_agg(b.id) INTO root_ids,node_ids FROM bounded b;
 IF cardinality(node_ids)>50000 THEN RAISE EXCEPTION 'release scope exceeds 50000 work nodes' USING ERRCODE='54000'; END IF;
 RETURN QUERY SELECT pair.root,n.id,k.slug,NOT EXISTS(
  SELECT 1 FROM nodes c JOIN node_kinds ck ON ck.tenant_id=c.tenant_id AND ck.id=c.kind_id
   WHERE c.parent_id=n.id AND c.deleted_at IS NULL AND ck.slug IN ('work','ticket','task')),n.project_id
  FROM unnest(root_ids,node_ids) pair(root,id) JOIN nodes n ON n.id=pair.id
  JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id;
END;
$$;

-- Invoker rights retain FORCE RLS. Stop at project boundaries, not arbitrary
-- depth: over-budget ancestry fails explicitly rather than choosing wrongly.
CREATE FUNCTION aeon_work_inherited_release(parent uuid, project uuid) RETURNS uuid
LANGUAGE plpgsql STABLE AS $$
DECLARE cur uuid := parent; rel uuid; next_parent uuid; depth integer := 0;
BEGIN
 WHILE cur IS NOT NULL LOOP
  depth := depth+1;
  IF depth>1000 THEN RAISE EXCEPTION 'release ancestry exceeds 1000 nodes' USING ERRCODE='54000'; END IF;
  SELECT p.release_node_id,n.parent_id INTO rel,next_parent
   FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
   LEFT JOIN work_parent_releases p ON p.tenant_id=n.tenant_id AND p.parent_node_id=n.id AND p.project_node_id=project
   WHERE n.id=cur AND n.project_id=project AND n.deleted_at IS NULL AND k.slug='work';
  IF NOT FOUND THEN RETURN NULL; END IF;
  IF rel IS NOT NULL THEN RETURN rel; END IF;
  cur := next_parent;
 END LOOP;
 RETURN NULL;
END;
$$;

-- Ordinary create/import/recurrence paths share this rule. The tree lock is
-- already held by structural writers; release transitions use the same lock.
-- The BEFORE trigger runs after tree_guard and before work_status_guard, so
-- its backlog state participates in the existing derivation/event snapshot.
CREATE FUNCTION aeon_work_release_note() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE rel uuid; state text;
BEGIN
 IF NEW.deleted_at IS NOT NULL OR NOT EXISTS(SELECT 1 FROM node_kinds WHERE id=NEW.kind_id AND tenant_id=NEW.tenant_id AND slug='work') THEN RETURN NEW; END IF;
 rel := aeon_work_inherited_release(NEW.parent_id,NEW.project_id);
 IF rel IS NULL THEN RETURN NEW; END IF;
 SELECT r.state INTO state FROM journey_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id
  WHERE r.release_node_id=rel AND r.project_node_id=NEW.project_id AND n.deleted_at IS NULL;
 IF state IS DISTINCT FROM 'planning' THEN
  NEW.state := 'backlog';
  NEW.fields := NEW.fields || jsonb_build_object('release_inheritance_note','parent_release_closed');
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER nodes_tree_release_inheritance_note BEFORE INSERT ON nodes FOR EACH ROW EXECUTE FUNCTION aeon_work_release_note();

CREATE FUNCTION aeon_work_release_inherit() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE rel uuid; state text;
BEGIN
 IF NEW.deleted_at IS NOT NULL OR NOT EXISTS(SELECT 1 FROM node_kinds WHERE id=NEW.kind_id AND tenant_id=NEW.tenant_id AND slug='work') THEN RETURN NEW; END IF;
 rel := aeon_work_inherited_release(NEW.parent_id,NEW.project_id);
 IF rel IS NULL THEN RETURN NEW; END IF;
 SELECT r.state INTO state FROM journey_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id
  WHERE r.release_node_id=rel AND r.project_node_id=NEW.project_id AND n.deleted_at IS NULL;
 IF state IS DISTINCT FROM 'planning' THEN RETURN NEW; END IF;
 INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source,scope_revision_required)
  VALUES(NEW.tenant_id,NEW.id,NEW.project_id,rel,
   (SELECT coalesce(max(walker_position)+1,0) FROM journey_tickets WHERE project_node_id=NEW.project_id),'manual',true);
 UPDATE journey_releases SET revision=revision+1 WHERE release_node_id=rel;
 UPDATE journey_projects SET revision=revision+1,updated_at=now() WHERE project_node_id=NEW.project_id;
 RETURN NEW;
END;
$$;
CREATE TRIGGER nodes_release_inheritance AFTER INSERT ON nodes FOR EACH ROW EXECUTE FUNCTION aeon_work_release_inherit();

-- AEON-596 integration seam: new captures accept work leaves. Existing frozen
-- note snapshots remain immutable; no historical payload is recaptured.
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
                WHEN n.id IS NOT NULL AND n.deleted_at IS NULL AND k.slug IN ('work','ticket') THEN (
                    SELECT coalesce(jsonb_object_agg(f.key, f.value), '{}'::jsonb)
                    FROM jsonb_each(n.fields) f
                    WHERE f.key IN ('pill_en', 'pill_de', 'benefit_en', 'benefit_de', 'hide_from_release_notes'))
                WHEN n.id IS NOT NULL THEN jsonb_build_object('hide_from_release_notes', coalesce(n.fields->'hide_from_release_notes', 'false'::jsonb))
                ELSE NULL END,
            'unavailable', CASE
                WHEN n.id IS NULL THEN 'Member is unavailable.'
                WHEN n.deleted_at IS NOT NULL THEN 'Member was deleted before capture.'
                WHEN coalesce(k.slug,'') NOT IN ('work','ticket') THEN 'Member is not a ticket.'
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
