-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-653 fix2, reserved 1237. Structural writers already hold the tenant/tree
-- fences; reconciliation runs before their event-counter append. Invoker rights
-- retain FORCE RLS. Historical memberships and frozen payloads are not rewritten.
SET LOCAL lock_timeout = '5s';

-- This migration is still unshipped on the AEON-653 branch. Retain the complete
-- typed projection, including access-review input, without making a parent a
-- live release member. The bounded object contains only journey_tickets data.
ALTER TABLE work_parent_releases ALTER COLUMN release_node_id DROP NOT NULL;
ALTER TABLE work_parent_releases ADD COLUMN retained_membership jsonb
 CHECK (jsonb_typeof(retained_membership)='object' AND octet_length(retained_membership::text)<=4096);

-- Explicit membership changes (including plan removal and compensating Undo)
-- supersede old intent. Invoker rights retain the table's tenant/project RLS.
CREATE FUNCTION aeon_work_sync_parent_release() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE work_parent_releases SET project_node_id=NEW.project_node_id,release_node_id=NEW.release_node_id,
  retained_membership=CASE WHEN retained_membership IS NOT NULL THEN to_jsonb(NEW) END
  WHERE tenant_id=NEW.tenant_id AND parent_node_id=NEW.ticket_node_id;
 RETURN NEW;
END;
$$;
CREATE TRIGGER journey_tickets_parent_release_change AFTER UPDATE OF release_node_id,project_node_id ON journey_tickets
 FOR EACH ROW WHEN (ROW(OLD.release_node_id,OLD.project_node_id) IS DISTINCT FROM ROW(NEW.release_node_id,NEW.project_node_id))
 EXECUTE FUNCTION aeon_work_sync_parent_release();

CREATE FUNCTION aeon_work_is_release_leaf(p_tenant uuid, p_node uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM nodes c JOIN node_kinds k ON k.tenant_id=c.tenant_id AND k.id=c.kind_id
  WHERE c.tenant_id=p_tenant AND c.parent_id=p_node AND c.deleted_at IS NULL AND k.slug IN ('work','ticket','task'));
$$;

CREATE OR REPLACE FUNCTION aeon_work_inherited_release(parent uuid, project uuid) RETURNS uuid
LANGUAGE plpgsql STABLE AS $$
DECLARE cur uuid := parent; rel uuid; next_parent uuid; placed boolean; depth integer := 0;
BEGIN
 WHILE cur IS NOT NULL LOOP
  depth := depth+1;
  IF depth>1000 THEN RAISE EXCEPTION 'release ancestry exceeds 1000 nodes' USING ERRCODE='54000'; END IF;
  -- Current parent intent (including explicit backlog) wins over a retained
  -- historical membership. Without intent, the membership still owns placement.
  SELECT CASE WHEN p.parent_node_id IS NOT NULL THEN p.release_node_id ELSE t.release_node_id END,
   n.parent_id,(t.ticket_node_id IS NOT NULL OR p.parent_node_id IS NOT NULL) INTO rel,next_parent,placed
   FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
   LEFT JOIN work_parent_releases p ON p.tenant_id=n.tenant_id AND p.parent_node_id=n.id AND p.project_node_id=project
   LEFT JOIN journey_tickets t ON t.tenant_id=n.tenant_id AND t.ticket_node_id=n.id AND t.project_node_id=project
   WHERE n.id=cur AND n.project_id=project AND n.deleted_at IS NULL AND k.slug='work';
  IF NOT FOUND THEN RETURN NULL; END IF;
  IF placed THEN RETURN rel; END IF;
  cur := next_parent;
 END LOOP;
 RETURN NULL;
END;
$$;

-- Reconcile only the nodes whose leaf status can change in one structural
-- write. A planning member becoming a parent retains its placement as intent.
-- Frozen/released rows remain historical evidence and live readers filter them.
CREATE FUNCTION aeon_work_reconcile_release_nodes(ids uuid[]) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE member record; candidate record; rel uuid; retained jsonb; changed uuid[] := '{}'; inserted integer;
BEGIN
 IF cardinality(ids)>100 THEN RAISE EXCEPTION 'release reconciliation exceeds 100 nodes' USING ERRCODE='54000'; END IF;
 FOR member IN
  SELECT t.* FROM journey_tickets t LEFT JOIN journey_releases r ON r.tenant_id=t.tenant_id AND r.release_node_id=t.release_node_id
  JOIN nodes n ON n.tenant_id=t.tenant_id AND n.id=t.ticket_node_id
  JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE n.id=ANY(ids) AND n.deleted_at IS NULL AND k.slug='work' AND (r.state='planning' OR t.release_node_id IS NULL)
   AND NOT aeon_work_is_release_leaf(n.tenant_id,n.id)
  ORDER BY t.release_node_id,t.ticket_node_id
 LOOP
  INSERT INTO work_parent_releases(tenant_id,parent_node_id,project_node_id,release_node_id,retained_membership)
   VALUES(member.tenant_id,member.ticket_node_id,member.project_node_id,member.release_node_id,to_jsonb(member))
   ON CONFLICT(tenant_id,parent_node_id) DO UPDATE SET project_node_id=excluded.project_node_id,
    release_node_id=excluded.release_node_id,retained_membership=excluded.retained_membership;
  DELETE FROM journey_tickets WHERE tenant_id=member.tenant_id AND ticket_node_id=member.ticket_node_id;
  changed := array_append(changed,member.release_node_id);
 END LOOP;
 FOR candidate IN
  SELECT n.* FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
   WHERE n.id=ANY(ids) AND n.deleted_at IS NULL AND k.slug='work' AND aeon_work_is_release_leaf(n.tenant_id,n.id)
    AND NOT EXISTS(SELECT 1 FROM journey_tickets t WHERE t.tenant_id=n.tenant_id AND t.ticket_node_id=n.id)
   ORDER BY n.id
 LOOP
  SELECT p.release_node_id,p.retained_membership INTO rel,retained FROM work_parent_releases p
   WHERE p.tenant_id=candidate.tenant_id AND p.parent_node_id=candidate.id AND p.project_node_id=candidate.project_id;
  IF NOT FOUND THEN rel := aeon_work_inherited_release(candidate.parent_id,candidate.project_id); END IF;
  IF rel IS NOT NULL AND NOT EXISTS(SELECT 1 FROM journey_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id
   WHERE r.release_node_id=rel AND r.project_node_id=candidate.project_id AND r.state='planning' AND n.deleted_at IS NULL) THEN CONTINUE; END IF;
  IF retained IS NOT NULL THEN
   -- Legacy/stale intent may name a different release than the saved approval.
   -- Keep every other field, but require fresh scope review at that destination.
   INSERT INTO journey_tickets SELECT saved.* FROM jsonb_populate_record(NULL::journey_tickets,
    jsonb_set(jsonb_set(retained,'{release_node_id}',coalesce(to_jsonb(rel),'null'::jsonb)),
     '{scope_revision_required}',CASE WHEN retained->'release_node_id' IS DISTINCT FROM coalesce(to_jsonb(rel),'null'::jsonb)
      THEN 'true'::jsonb ELSE coalesce(retained->'scope_revision_required','true'::jsonb) END)) saved
    WHERE saved.tenant_id=candidate.tenant_id AND saved.ticket_node_id=candidate.id AND saved.project_node_id=candidate.project_id
    ON CONFLICT(tenant_id,ticket_node_id) DO NOTHING;
  ELSIF rel IS NOT NULL THEN
   INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source,scope_revision_required)
    VALUES(candidate.tenant_id,candidate.id,candidate.project_id,rel,
     (SELECT coalesce(max(walker_position)+1,0) FROM journey_tickets WHERE project_node_id=candidate.project_id),'manual',true)
    ON CONFLICT(tenant_id,ticket_node_id) DO NOTHING;
  ELSE CONTINUE;
  END IF;
  GET DIAGNOSTICS inserted = ROW_COUNT;
  IF inserted>0 THEN changed := array_append(changed,rel); END IF;
 END LOOP;
 -- Stable ordering, one revision per affected release/project in this write.
 FOR member IN SELECT r.release_node_id,r.project_node_id FROM journey_releases r WHERE r.release_node_id=ANY(changed) ORDER BY r.release_node_id LOOP
  UPDATE journey_releases SET revision=revision+1,access_required=EXISTS(
   SELECT 1 FROM journey_tickets t JOIN nodes n ON n.tenant_id=t.tenant_id AND n.id=t.ticket_node_id
    WHERE t.release_node_id=member.release_node_id AND t.access_change AND n.deleted_at IS NULL AND aeon_work_is_release_leaf(n.tenant_id,n.id))
   WHERE release_node_id=member.release_node_id;
 END LOOP;
 UPDATE journey_projects SET revision=revision+1,updated_at=now()
  WHERE project_node_id IN (SELECT project_node_id FROM journey_releases WHERE release_node_id=ANY(changed));
END;
$$;

CREATE OR REPLACE FUNCTION aeon_work_release_inherit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  PERFORM aeon_work_reconcile_release_nodes(ARRAY[NEW.id,NEW.parent_id,OLD.parent_id]);
 ELSE
  PERFORM aeon_work_reconcile_release_nodes(ARRAY[NEW.id,NEW.parent_id]);
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER nodes_release_leaf_change AFTER UPDATE OF parent_id,kind_id,deleted_at ON nodes
 FOR EACH ROW WHEN (ROW(OLD.parent_id,OLD.kind_id,OLD.deleted_at) IS DISTINCT FROM ROW(NEW.parent_id,NEW.kind_id,NEW.deleted_at))
 EXECUTE FUNCTION aeon_work_release_inherit();

-- Repair already-created planning parents, under the migration writer pause.
-- One bounded node per call; never alter historical released scope or snapshots.
DO $$
DECLARE t record; member record; leaf uuid; leaves uuid[];
 prior_tenant text := current_setting('aeon.tenant_id',true);
 prior_visible text := current_setting('aeon.visible_projects',true);
BEGIN
 FOR t IN SELECT id FROM tenants ORDER BY id LOOP
  PERFORM set_config('aeon.tenant_id',t.id::text,true),set_config('aeon.visible_projects','*',true);
  FOR member IN SELECT jt.ticket_node_id FROM journey_tickets jt JOIN journey_releases r ON r.tenant_id=jt.tenant_id AND r.release_node_id=jt.release_node_id
   WHERE jt.tenant_id=t.id AND r.state='planning' AND NOT aeon_work_is_release_leaf(jt.tenant_id,jt.ticket_node_id) ORDER BY jt.ticket_node_id
  LOOP
   SELECT array_agg(s.id) INTO leaves FROM (SELECT id FROM aeon_work_release_scope(ARRAY[member.ticket_node_id]) WHERE is_leaf LIMIT 1001) s;
   IF cardinality(leaves)>1000 THEN RAISE EXCEPTION 'release repair exceeds 1000 leaves' USING ERRCODE='54000'; END IF;
   PERFORM aeon_work_reconcile_release_nodes(ARRAY[member.ticket_node_id]);
   -- Old direct leaf placements may have had children before intent existed.
   -- Fill only absent memberships; explicit backlog rows stay excluded.
   FOREACH leaf IN ARRAY coalesce(leaves,'{}'::uuid[]) LOOP
    PERFORM aeon_work_reconcile_release_nodes(ARRAY[leaf]);
   END LOOP;
  END LOOP;
 END LOOP;
 PERFORM set_config('aeon.tenant_id',coalesce(prior_tenant,''),true),set_config('aeon.visible_projects',coalesce(prior_visible,''),true);
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
            WHERE t.tenant_id = r.tenant_id AND t.project_node_id = r.project_node_id AND t.release_node_id = r.release_node_id
              AND aeon_work_is_release_leaf(t.tenant_id,t.ticket_node_id)), '[]'::jsonb))
    FROM journey_releases r
    JOIN nodes rn ON rn.tenant_id = r.tenant_id AND rn.id = r.release_node_id AND rn.deleted_at IS NULL
    JOIN nodes pn ON pn.tenant_id = r.tenant_id AND pn.id = r.project_node_id AND pn.deleted_at IS NULL
    WHERE r.tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid
      AND r.project_node_id = p_project
      AND r.release_node_id = p_release;
$$;
