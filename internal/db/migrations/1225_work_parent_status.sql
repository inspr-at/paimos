-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-650. Reserved slot 1225. No permanent user-data storage is added.
-- AEON-429 is a merge dependency: its features table may not exist yet.
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION aeon_work_status_enabled(p_project uuid) RETURNS boolean
LANGUAGE plpgsql STABLE AS $$
DECLARE enabled boolean;
BEGIN
 IF to_regclass('public.features') IS NULL THEN RETURN false; END IF;
 EXECUTE 'SELECT coalesce(
   (SELECT enabled FROM features WHERE tenant_id=$1 AND key=''work-parent-status'' AND project_id=$2),
   (SELECT enabled FROM features WHERE tenant_id=$1 AND key=''work-parent-status'' AND project_id IS NULL), false)'
 INTO enabled USING NULLIF(current_setting('aeon.tenant_id',true),'')::uuid,p_project;
 RETURN coalesce(enabled,false);
END;
$$;

-- Custom categories override the fixed vocabulary, just as the count buckets do.
-- Delivered/Accepted retain their step only within the Done category.
CREATE FUNCTION aeon_work_status_category(p_state text, p_schema jsonb) RETURNS text
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE norm text := regexp_replace(lower(btrim(p_state)), '[[:space:]-]+','_','g'); category text;
BEGIN
 SELECT regexp_replace(lower(btrim(e->>'category')), '[[:space:]-]+','_','g') INTO category
 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(p_schema->'states')='array'
   THEN p_schema->'states' ELSE '[]'::jsonb END) e
 WHERE regexp_replace(lower(btrim(e->>'state')), '[[:space:]-]+','_','g')=norm
   AND coalesce(e->>'category','')<>'' LIMIT 1;
 category := CASE category WHEN 'doing' THEN 'in_progress' WHEN 'progress' THEN 'in_progress'
   WHEN 'canceled' THEN 'cancelled' ELSE category END;
 IF category IS NULL OR category NOT IN ('open','in_progress','blocked','done','cancelled','archived') THEN
   category := CASE norm WHEN 'inprogress' THEN 'in_progress' WHEN 'progress' THEN 'in_progress'
    WHEN 'active' THEN 'in_progress' WHEN 'qa' THEN 'in_progress' WHEN 'accepted' THEN 'done'
    WHEN 'delivered' THEN 'done' WHEN 'canceled' THEN 'cancelled'
    WHEN 'done' THEN 'done' WHEN 'cancelled' THEN 'cancelled' WHEN 'archived' THEN 'archived'
    WHEN 'blocked' THEN 'blocked' WHEN 'in_progress' THEN 'in_progress' ELSE 'open' END;
 END IF;
 RETURN CASE WHEN category='done' AND norm IN ('delivered','accepted') THEN norm ELSE category END;
END;
$$;

-- Called at transaction entry, before any resource or event-counter lock.
-- Follow the existing paired-writer protocol: pairing -> tree -> tenant -> rows.
-- Every work row is prelocked once (bounded), including both sides of a move.
-- This also permits final derivation after an existing writer has emitted its
-- cause: it acquires no new resource locks after the event counter.
CREATE FUNCTION aeon_work_status_begin() RETURNS boolean
LANGUAGE plpgsql AS $$
DECLARE prior_visible text := current_setting('aeon.visible_projects',true);
 prior_system text := current_setting('aeon.system',true); active boolean; total integer;
BEGIN
 IF current_setting('aeon.work_status_entered',true)=current_setting('aeon.tenant_id',true) THEN RETURN true; END IF;
 IF to_regclass('public.features') IS NULL THEN RETURN false; END IF;
 PERFORM set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true);
 EXECUTE 'SELECT EXISTS(SELECT 1 FROM features WHERE tenant_id=$1 AND key=''work-parent-status'' AND enabled=true)'
 INTO active USING current_setting('aeon.tenant_id')::uuid;
 IF NOT active THEN
  PERFORM set_config('aeon.visible_projects',coalesce(prior_visible,''),true),set_config('aeon.system',coalesce(prior_system,''),true);
  RETURN false;
 END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('aeon-pairing:'||current_setting('aeon.tenant_id'),0));
 PERFORM pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id'),0));
 PERFORM 1 FROM tenants WHERE id=current_setting('aeon.tenant_id')::uuid FOR NO KEY UPDATE;
 SELECT count(*) INTO total FROM (SELECT n.id FROM nodes n JOIN node_kinds k USING(tenant_id)
   WHERE k.id=n.kind_id AND n.tenant_id=current_setting('aeon.tenant_id')::uuid AND k.slug='work' LIMIT 50001) s;
 IF total>50000 THEN RAISE EXCEPTION 'work status scope exceeds 50000 nodes' USING ERRCODE='54000'; END IF;
 PERFORM n.id FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND k.slug='work'
  ORDER BY n.id FOR NO KEY UPDATE OF n;
 -- Resolve the actor now, before any writer takes another resource lock.
 PERFORM pg_advisory_xact_lock(hashtextextended('aeon-system-actor:'||current_setting('aeon.tenant_id'),0));
 IF to_regclass('pg_temp.aeon_work_changes') IS NULL THEN
  CREATE TEMP TABLE aeon_work_changes(tenant_id uuid NOT NULL,id uuid NOT NULL,before jsonb,after jsonb,
    PRIMARY KEY(tenant_id,id)) ON COMMIT DROP;
  CREATE TEMP TABLE aeon_work_causes(tenant_id uuid NOT NULL,id bigint NOT NULL,node_id uuid,type text NOT NULL,
    PRIMARY KEY(tenant_id,id)) ON COMMIT DROP;
  CREATE TEMP TABLE aeon_work_affected(tenant_id uuid,id uuid,trigger_id uuid,PRIMARY KEY(tenant_id,id,trigger_id)) ON COMMIT DROP;
  CREATE TEMP TABLE aeon_work_settings(tenant_id uuid PRIMARY KEY) ON COMMIT DROP;
 END IF;
 PERFORM set_config('aeon.work_status_entered',current_setting('aeon.tenant_id'),true);
 PERFORM set_config('aeon.visible_projects',coalesce(prior_visible,''),true),set_config('aeon.system',coalesce(prior_system,''),true);
 RETURN true;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.visible_projects',coalesce(prior_visible,''),true),set_config('aeon.system',coalesce(prior_system,''),true);
 RAISE;
END;
$$;

CREATE FUNCTION aeon_work_status_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE slug text; prior_visible text := current_setting('aeon.visible_projects',true);
 prior_system text := current_setting('aeon.system',true); a jsonb; b jsonb;
BEGIN
 IF current_setting('aeon.work_status_deriving',true)='on' THEN RETURN NEW; END IF;
 SELECT k.slug INTO slug FROM node_kinds k WHERE k.tenant_id=NEW.tenant_id AND k.id=NEW.kind_id;
 IF slug<>'work' AND (TG_OP='INSERT' OR NOT EXISTS(SELECT 1 FROM node_kinds k WHERE k.id=OLD.kind_id AND k.slug='work')) THEN RETURN NEW; END IF;
 PERFORM set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true);
 IF current_setting('aeon.work_status_entered',true) IS DISTINCT FROM NEW.tenant_id::text
    AND NOT aeon_work_status_enabled(NEW.project_id) THEN
  PERFORM set_config('aeon.visible_projects',coalesce(prior_visible,''),true),set_config('aeon.system',coalesce(prior_system,''),true);
  RETURN NEW;
 END IF;
 IF current_setting('aeon.work_status_entered',true) IS DISTINCT FROM NEW.tenant_id::text THEN
  RAISE EXCEPTION 'work status transaction protocol required' USING ERRCODE='55000';
 END IF;
 IF TG_OP='UPDATE' AND NEW.state IS DISTINCT FROM OLD.state AND aeon_work_status_enabled(NEW.project_id) AND EXISTS (
   SELECT 1 FROM nodes c JOIN node_kinds k ON k.tenant_id=c.tenant_id AND k.id=c.kind_id
    WHERE c.tenant_id=NEW.tenant_id AND c.parent_id=NEW.id AND c.deleted_at IS NULL AND k.slug='work') THEN
  RAISE EXCEPTION 'parent status follows its children' USING ERRCODE='P0001';
 END IF;
 IF TG_OP='UPDATE' AND ROW(NEW.title,NEW.body,NEW.state,NEW.fields,NEW.parent_id,NEW.deleted_at,NEW.kind_id,NEW.human_check)
  IS DISTINCT FROM ROW(OLD.title,OLD.body,OLD.state,OLD.fields,OLD.parent_id,OLD.deleted_at,OLD.kind_id,OLD.human_check) THEN
  NEW.updated_at := greatest(clock_timestamp(),NEW.updated_at,OLD.updated_at+interval '1 microsecond');
 END IF;
 a := jsonb_build_object('id',NEW.id,'parent_id',NEW.parent_id,'state',NEW.state,'deleted_at',NEW.deleted_at,'updated_at',NEW.updated_at);
 IF TG_OP='UPDATE' THEN b := jsonb_build_object('id',OLD.id,'parent_id',OLD.parent_id,'state',OLD.state,'deleted_at',OLD.deleted_at,'updated_at',OLD.updated_at); END IF;
 IF TG_OP='INSERT' OR NEW.state IS DISTINCT FROM OLD.state OR NEW.parent_id IS DISTINCT FROM OLD.parent_id
   OR NEW.deleted_at IS DISTINCT FROM OLD.deleted_at OR NEW.kind_id IS DISTINCT FROM OLD.kind_id THEN
  INSERT INTO pg_temp.aeon_work_changes VALUES(NEW.tenant_id,NEW.id,b,a)
    ON CONFLICT(tenant_id,id) DO UPDATE SET after=EXCLUDED.after;
 END IF;
 PERFORM set_config('aeon.visible_projects',coalesce(prior_visible,''),true),set_config('aeon.system',coalesce(prior_system,''),true);
 RETURN NEW;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.visible_projects',coalesce(prior_visible,''),true),set_config('aeon.system',coalesce(prior_system,''),true);
 RAISE;
END;
$$;
CREATE TRIGGER nodes_work_status_guard BEFORE INSERT OR UPDATE ON nodes FOR EACH ROW EXECUTE FUNCTION aeon_work_status_guard();

CREATE FUNCTION aeon_work_status_settings() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.slug='work' AND NEW.field_schema->'states' IS DISTINCT FROM OLD.field_schema->'states'
  AND current_setting('aeon.work_status_entered',true)=NEW.tenant_id::text THEN
  INSERT INTO pg_temp.aeon_work_settings VALUES(NEW.tenant_id) ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER node_kinds_work_status_settings AFTER UPDATE ON node_kinds FOR EACH ROW EXECUTE FUNCTION aeon_work_status_settings();

CREATE FUNCTION aeon_work_status_cause() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF current_setting('aeon.work_status_entered',true) IS DISTINCT FROM NEW.tenant_id::text THEN RETURN NEW; END IF;
 IF NEW.type IN ('node.created','node.updated','node.deleted','node.moved','node.project_moved','node.bulk_changed','node.kind_changed','import.node_created','import.node_updated','import.parent_changed','status_autopilot.changed','status_autopilot.undone','kind.updated','feature.updated')
  AND (NEW.node_id IN (SELECT id FROM pg_temp.aeon_work_changes WHERE tenant_id=NEW.tenant_id)
    OR NEW.type IN ('node.bulk_changed','kind.updated','feature.updated')) THEN
  IF (SELECT count(*) FROM pg_temp.aeon_work_causes)>=10000 THEN
   RAISE EXCEPTION 'work status cause scope exceeds 10000 events' USING ERRCODE='54000';
  END IF;
  INSERT INTO pg_temp.aeon_work_causes VALUES(NEW.tenant_id,NEW.id,NEW.node_id,NEW.type);
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER events_work_status_cause AFTER INSERT ON events FOR EACH ROW EXECUTE FUNCTION aeon_work_status_cause();

CREATE FUNCTION aeon_work_status_flush() RETURNS void LANGUAGE plpgsql AS $$
DECLARE prior_visible text := current_setting('aeon.visible_projects',true);
 prior_system text := current_setting('aeon.system',true); target_tenant uuid := current_setting('aeon.tenant_id')::uuid;
 r record; cats text[]; target_state text; reason text; before_snap jsonb; after_snap jsonb; actor uuid;
 cause_id bigint; change_count integer; max_depth integer;
BEGIN
 IF current_setting('aeon.work_status_entered',true) IS DISTINCT FROM target_tenant::text THEN RETURN; END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_temp.aeon_work_changes WHERE tenant_id=target_tenant)
  AND NOT EXISTS(SELECT 1 FROM pg_temp.aeon_work_settings WHERE tenant_id=target_tenant) THEN RETURN; END IF;
 PERFORM set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true),set_config('aeon.work_status_deriving','on',true);
 -- Walk each work edge once; projects and other kinds end work ancestry.
 -- Explicit limits reject the transaction rather than produce a partial status.
 IF to_regclass('pg_temp.aeon_work_order') IS NULL THEN
  CREATE TEMP TABLE aeon_work_order(tenant_id uuid,id uuid,depth integer,PRIMARY KEY(tenant_id,id)) ON COMMIT DROP;
 END IF;
 DELETE FROM pg_temp.aeon_work_order WHERE tenant_id=target_tenant;
 INSERT INTO pg_temp.aeon_work_order
 WITH RECURSIVE work AS MATERIALIZED (
  SELECT n.id,n.parent_id FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE n.tenant_id=target_tenant AND k.slug='work' AND n.deleted_at IS NULL
 ), tree AS (
  SELECT w.id,1 depth FROM work w WHERE NOT EXISTS(SELECT 1 FROM work p WHERE p.id=w.parent_id)
  UNION ALL SELECT w.id,t.depth+1 FROM tree t JOIN work w ON w.parent_id=t.id WHERE t.depth<=1000
 ) SELECT target_tenant,id,depth FROM tree;
 SELECT coalesce(max(depth),0) INTO max_depth FROM pg_temp.aeon_work_order WHERE tenant_id=target_tenant;
 IF max_depth>1000 THEN RAISE EXCEPTION 'work status depth exceeds 1000' USING ERRCODE='54000'; END IF;
 actor := aeon_authz_system_actor(target_tenant);
 IF (SELECT count(*) FROM pg_temp.aeon_work_changes WHERE tenant_id=target_tenant)>1000 THEN RAISE EXCEPTION 'work status change scope exceeds 1000 nodes' USING ERRCODE='54000'; END IF;
 DELETE FROM pg_temp.aeon_work_affected WHERE tenant_id=target_tenant;
 INSERT INTO pg_temp.aeon_work_affected
 WITH RECURSIVE ancestors(id,trigger_id) AS (
  (SELECT id,id FROM pg_temp.aeon_work_changes WHERE tenant_id=target_tenant
  UNION
  SELECT (before->>'parent_id')::uuid,id FROM pg_temp.aeon_work_changes WHERE tenant_id=target_tenant
  UNION SELECT (after->>'parent_id')::uuid,id FROM pg_temp.aeon_work_changes WHERE tenant_id=target_tenant)
  UNION
  SELECT n.parent_id,a.trigger_id FROM ancestors a JOIN nodes n ON n.tenant_id=target_tenant AND n.id=a.id
   JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='work' AND n.parent_id IS NOT NULL
 ) SELECT target_tenant,id,trigger_id FROM ancestors WHERE id IS NOT NULL;

 FOR r IN SELECT n.id,n.state,n.project_id,n.updated_at,k.field_schema
  FROM pg_temp.aeon_work_order o JOIN nodes n ON n.tenant_id=o.tenant_id AND n.id=o.id
  JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE o.tenant_id=target_tenant AND (
    EXISTS(SELECT 1 FROM pg_temp.aeon_work_affected a WHERE a.tenant_id=target_tenant AND a.id=n.id)
    OR EXISTS(SELECT 1 FROM pg_temp.aeon_work_settings s WHERE s.tenant_id=target_tenant))
  ORDER BY o.depth DESC,n.id LOOP
  IF NOT aeon_work_status_enabled(r.project_id) THEN CONTINUE; END IF;
  SELECT array_agg(category) FILTER(WHERE category<>'archived') INTO cats FROM (
   SELECT aeon_work_status_category(c.state,k.field_schema) category FROM nodes c
   JOIN node_kinds k ON k.tenant_id=c.tenant_id AND k.id=c.kind_id
   WHERE c.tenant_id=target_tenant AND c.parent_id=r.id AND c.deleted_at IS NULL AND k.slug='work') s;
  -- A former parent with no remaining work children retains its last state.
  -- Retention has its own audit type, never a fictitious status transition.
  IF cats IS NULL THEN
   IF EXISTS(SELECT 1 FROM pg_temp.aeon_work_changes c WHERE c.tenant_id=target_tenant
      AND c.before->>'parent_id'=r.id::text AND (c.after->>'parent_id' IS DISTINCT FROM c.before->>'parent_id'
        OR c.after->>'deleted_at' IS DISTINCT FROM c.before->>'deleted_at')) THEN
    INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after,metadata)
    VALUES(target_tenant,actor,r.id,'status_autopilot.retained',jsonb_build_object('id',r.id,'state',r.state),
      jsonb_build_object('rule','work_parent','rule_version',1,'reason','No remaining work children; the last derived status is retained.','affected_nodes',jsonb_build_array(r.id)));
   END IF;
   CONTINUE;
  END IF;
  reason := 'Follows the current work children.';
  IF 'in_progress'=ANY(cats) THEN target_state := 'in_progress';
  ELSIF 'blocked'=ANY(cats) THEN target_state := 'blocked';
  ELSIF 'open'=ANY(cats) THEN target_state := 'open';
  ELSIF cats <@ ARRAY['cancelled']::text[] THEN target_state := 'cancelled';
  ELSIF cats <@ ARRAY['accepted']::text[] THEN target_state := 'accepted';
  ELSIF cats <@ ARRAY['accepted','delivered']::text[] THEN target_state := 'delivered';
  ELSE target_state := 'done'; END IF;
  -- Compare with the freshly written descendants, never the caller's snapshot.
  SELECT jsonb_build_object('id',id,'state',state,'updated_at',updated_at) INTO before_snap
    FROM nodes WHERE tenant_id=target_tenant AND id=r.id;
  IF before_snap->>'state'=target_state THEN CONTINUE; END IF;
  UPDATE nodes SET state=target_state WHERE tenant_id=target_tenant AND id=r.id;
  SELECT jsonb_build_object('id',id,'state',state,'updated_at',updated_at) INTO after_snap
    FROM nodes WHERE tenant_id=target_tenant AND id=r.id;
  SELECT count(*),min(c.id) INTO change_count,cause_id FROM pg_temp.aeon_work_causes c
    JOIN events e ON e.tenant_id=c.tenant_id AND e.id=c.id
    WHERE c.tenant_id=target_tenant
     AND (c.type='node.bulk_changed' OR NOT EXISTS(SELECT 1 FROM pg_temp.aeon_work_causes bulk_c WHERE bulk_c.tenant_id=target_tenant AND bulk_c.type='node.bulk_changed'))
     AND EXISTS(SELECT 1 FROM pg_temp.aeon_work_affected a
     WHERE a.tenant_id=target_tenant AND a.id=r.id AND (a.trigger_id=c.node_id OR a.trigger_id=ANY(e.node_refs)));
  IF change_count<>1 THEN cause_id:=NULL; END IF;
  INSERT INTO events(tenant_id,actor_principal_id,node_id,type,before,after,metadata)
  VALUES(target_tenant,actor,r.id,'status_autopilot.derived',before_snap,after_snap,
   jsonb_build_object('job','status-autopilot','rule','work_parent','rule_version',1,'reason',reason,
    'cause_event_id',cause_id,'affected_nodes',jsonb_build_array(r.id),'coalesced',true));
 END LOOP;
 DELETE FROM pg_temp.aeon_work_changes WHERE tenant_id=target_tenant;
 DELETE FROM pg_temp.aeon_work_causes WHERE tenant_id=target_tenant;
 DELETE FROM pg_temp.aeon_work_settings WHERE tenant_id=target_tenant;
 PERFORM set_config('aeon.work_status_deriving','',true),set_config('aeon.visible_projects',coalesce(prior_visible,''),true),set_config('aeon.system',coalesce(prior_system,''),true);
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.work_status_deriving','',true),set_config('aeon.visible_projects',coalesce(prior_visible,''),true),set_config('aeon.system',coalesce(prior_system,''),true);
 RAISE;
END;
$$;

-- Authorization still belongs to the caller's write. This predicate exposes
-- only whether its already-authorized target has work children, never IDs.
CREATE FUNCTION aeon_work_status_is_parent(p_node uuid) RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE prior_visible text := current_setting('aeon.visible_projects',true);
 prior_system text := current_setting('aeon.system',true); project uuid; work boolean; answer boolean;
BEGIN
 SELECT n.project_id,k.slug='work' INTO project,work FROM nodes n JOIN node_kinds k
  ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=p_node;
 IF NOT coalesce(work,false) THEN RETURN false; END IF;
 PERFORM set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true);
 answer := aeon_work_status_enabled(project) AND EXISTS(SELECT 1 FROM nodes c JOIN node_kinds k
  ON k.tenant_id=c.tenant_id AND k.id=c.kind_id WHERE c.parent_id=p_node AND c.deleted_at IS NULL AND k.slug='work');
 PERFORM set_config('aeon.visible_projects',coalesce(prior_visible,''),true),set_config('aeon.system',coalesce(prior_system,''),true);
 RETURN answer;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.visible_projects',coalesce(prior_visible,''),true),set_config('aeon.system',coalesce(prior_system,''),true);
 RAISE;
END;
$$;

-- Derived facts are visible wherever their parent is visible, with no cause
-- node in the snapshot and the existing referenced-node checks intact.
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
                            'kind', 'kinds', 'profile')
                         OR type IN ('status_autopilot.changed', 'status_autopilot.skipped', 'status_autopilot.derived', 'status_autopilot.retained', 'status_autopilot.causal_undo')
                         OR actor_principal_id = ANY ((SELECT aeon_current_principals())::uuid[]))
            END
            AND (cardinality(node_refs) = 0
                 OR NOT EXISTS (SELECT 1 FROM unnest(node_refs) AS ref(id)
                                WHERE ref.id NOT IN (SELECT n.id FROM nodes n)))));
