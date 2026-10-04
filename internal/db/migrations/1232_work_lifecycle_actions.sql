-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-652 durable, person-authorized intents. Draft titles/body are personal
-- data; identifiers/state are tenant metadata. No process signal authority.
SET LOCAL lock_timeout = '5s';
CREATE TABLE work_lifecycle_actions (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL,
 node_id uuid NOT NULL,
 requested_by uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('split','cancel')),
 state text NOT NULL DEFAULT 'waiting' CHECK(state IN ('waiting','completed','abandoned')),
 targets jsonb NOT NULL CHECK(jsonb_typeof(targets)='array' AND jsonb_array_length(targets)<=100),
 children jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(children)='array' AND jsonb_array_length(children)<=20),
 result jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(result)='array' AND jsonb_array_length(result)<=100),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 completed_at timestamptz,
 next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,node_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY(tenant_id,requested_by) REFERENCES principals(tenant_id,id)
);
ALTER TABLE work_lifecycle_actions ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_lifecycle_actions FORCE ROW LEVEL SECURITY;
CREATE POLICY work_lifecycle_tenant ON work_lifecycle_actions USING (
 tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid AND EXISTS (
 SELECT 1 FROM nodes n WHERE n.id=node_id AND n.tenant_id=work_lifecycle_actions.tenant_id));
CREATE INDEX work_lifecycle_waiting_tenant ON work_lifecycle_actions(tenant_id,next_attempt_at,id) WHERE state='waiting';
CREATE UNIQUE INDEX work_lifecycle_pending ON work_lifecycle_actions(tenant_id,node_id) WHERE state='waiting';

-- Fence new bindings and regrouping while the original person's intent waits.
-- Completed actions retain immutable targets/results for replay and audit.
CREATE FUNCTION aeon_work_pending(p_node uuid) RETURNS uuid LANGUAGE plpgsql AS $$
DECLARE answer uuid; ancestors uuid[]; v text:=current_setting('aeon.visible_projects',true); s text:=current_setting('aeon.system',true);
BEGIN
 PERFORM set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true);
 WITH RECURSIVE chain(id,parent_id) AS (
  SELECT id,parent_id FROM nodes WHERE id=p_node
  UNION SELECT n.id,n.parent_id FROM nodes n JOIN chain c ON n.id=c.parent_id
 ) SELECT array_agg(id) INTO ancestors FROM (SELECT id FROM chain LIMIT 50001) bounded;
 IF cardinality(ancestors)>50000 THEN RAISE EXCEPTION 'work action ancestry exceeds scope budget' USING ERRCODE='54000'; END IF;
 SELECT a.id INTO answer FROM work_lifecycle_actions a
 WHERE a.state='waiting' AND (a.node_id=ANY(ancestors) OR EXISTS(
  SELECT 1 FROM jsonb_array_elements(a.targets) t WHERE t->>'id'=p_node::text)) LIMIT 1;
 PERFORM set_config('aeon.visible_projects',coalesce(v,''),true),set_config('aeon.system',coalesce(s,''),true);
 RETURN answer;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.visible_projects',coalesce(v,''),true),set_config('aeon.system',coalesce(s,''),true); RAISE;
END;
$$;
CREATE OR REPLACE FUNCTION aeon_require_work_leaf(p_node uuid) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.id=p_node AND k.slug='work') THEN
  IF NOT aeon_work_leaf(p_node) THEN
   RAISE EXCEPTION 'agents work on leaves only' USING ERRCODE='23514',CONSTRAINT='work_leaf_required';
  END IF;
  IF aeon_work_pending(p_node) IS NOT NULL THEN
   RAISE EXCEPTION 'work is waiting for graceful handover' USING ERRCODE='23514',CONSTRAINT='work_handover_pending';
  END IF;
 END IF;
END;
$$;
CREATE FUNCTION aeon_guard_work_intent_tree() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE pending uuid;
BEGIN
 IF TG_OP='UPDATE' AND (NEW.parent_id IS DISTINCT FROM OLD.parent_id OR NEW.kind_id<>OLD.kind_id OR NEW.deleted_at IS DISTINCT FROM OLD.deleted_at) THEN
  pending:=aeon_work_pending(NEW.id);
  IF pending IS NOT NULL AND pending::text IS DISTINCT FROM current_setting('aeon.work_lifecycle_action',true) THEN
   RAISE EXCEPTION 'finish the pending work action before regrouping' USING ERRCODE='23514',CONSTRAINT='work_handover_pending';
  END IF;
 END IF;
 IF NEW.deleted_at IS NULL AND NEW.parent_id IS NOT NULL AND (TG_OP='INSERT' OR NEW.parent_id IS DISTINCT FROM OLD.parent_id OR NEW.kind_id<>OLD.kind_id OR OLD.deleted_at IS NOT NULL)
  AND EXISTS(SELECT 1 FROM node_kinds WHERE id=NEW.kind_id AND slug='work') THEN
  pending:=aeon_work_pending(NEW.parent_id);
  IF pending IS NOT NULL AND pending::text IS DISTINCT FROM current_setting('aeon.work_lifecycle_action',true) THEN
   RAISE EXCEPTION 'finish the pending work action before adding children' USING ERRCODE='23514',CONSTRAINT='work_handover_pending';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER nodes_work_intent_tree BEFORE INSERT OR UPDATE OF parent_id,kind_id,deleted_at ON nodes FOR EACH ROW EXECUTE FUNCTION aeon_guard_work_intent_tree();

CREATE FUNCTION aeon_work_scope_complete(p_node uuid) RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE visible_count bigint; total_count bigint; v text:=current_setting('aeon.visible_projects',true); s text:=current_setting('aeon.system',true);
BEGIN
 SELECT count(*) INTO visible_count FROM aeon_work_scope(ARRAY[p_node]);
 PERFORM set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true);
 SELECT count(*) INTO total_count FROM aeon_work_scope(ARRAY[p_node]);
 PERFORM set_config('aeon.visible_projects',coalesce(v,''),true),set_config('aeon.system',coalesce(s,''),true);
 RETURN visible_count=total_count;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.visible_projects',coalesce(v,''),true),set_config('aeon.system',coalesce(s,''),true); RAISE;
END;
$$;

-- Fence the ORIGINAL binding too. Detaching/rebinding is not proof of stop.
-- No generation identifiers or historical bindings are rewritten or copied.
CREATE FUNCTION aeon_work_binding_targets(p_ticket uuid,p_order uuid,p_run uuid)
RETURNS uuid[] LANGUAGE plpgsql AS $$
DECLARE answer uuid[]; v text:=current_setting('aeon.visible_projects',true); s text:=current_setting('aeon.system',true);
BEGIN
 PERFORM set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true);
 SELECT coalesce(array_agg(DISTINCT target) FILTER(WHERE target IS NOT NULL),ARRAY[]::uuid[]) INTO answer
 FROM (
  SELECT p_ticket AS target
  UNION ALL SELECT p_order
  UNION ALL SELECT n.parent_id FROM nodes n JOIN node_kinds k ON k.id=n.kind_id
   WHERE n.id IN(p_ticket,p_order) AND k.slug='work_order'
  UNION ALL SELECT r.queue_node_id FROM agent_runs r WHERE r.id=p_run
  UNION ALL SELECT n.parent_id FROM agent_runs r JOIN nodes n ON n.id=r.work_order_id WHERE r.id=p_run
 ) bindings;
 PERFORM set_config('aeon.visible_projects',coalesce(v,''),true),set_config('aeon.system',coalesce(s,''),true);
 RETURN answer;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.visible_projects',coalesce(v,''),true),set_config('aeon.system',coalesce(s,''),true); RAISE;
END;
$$;
CREATE FUNCTION aeon_guard_pending_work_binding() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE targets uuid[]; target uuid;
BEGIN
 IF TG_TABLE_NAME='harness_sessions' THEN
  IF NEW.ticket_node_id IS NOT DISTINCT FROM OLD.ticket_node_id
   AND NEW.work_order_id IS NOT DISTINCT FROM OLD.work_order_id
   AND NEW.run_id IS NOT DISTINCT FROM OLD.run_id THEN RETURN NEW; END IF;
  IF aeon_work_session_stopped(OLD.stopped_at,OLD.stop_reason) THEN RETURN NEW; END IF;
  targets:=aeon_work_binding_targets(OLD.ticket_node_id,OLD.work_order_id,OLD.run_id);
 ELSE
  IF NEW.queue_node_id IS NOT DISTINCT FROM OLD.queue_node_id
   AND NEW.work_order_id IS NOT DISTINCT FROM OLD.work_order_id THEN RETURN NEW; END IF;
  targets:=aeon_work_binding_targets(OLD.queue_node_id,OLD.work_order_id,OLD.id);
 END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id'),0));
 FOREACH target IN ARRAY targets LOOP
  IF aeon_work_pending(target) IS NOT NULL THEN
   RAISE EXCEPTION 'confirm the original generation stopped before changing its work binding'
    USING ERRCODE='23514',CONSTRAINT='work_handover_pending';
  END IF;
 END LOOP;
 RETURN NEW;
END;
$$;
CREATE TRIGGER harness_pending_work_binding BEFORE UPDATE OF ticket_node_id,work_order_id,run_id
 ON harness_sessions FOR EACH ROW EXECUTE FUNCTION aeon_guard_pending_work_binding();
CREATE TRIGGER runs_pending_work_binding BEFORE UPDATE OF queue_node_id,work_order_id
 ON agent_runs FOR EACH ROW EXECUTE FUNCTION aeon_guard_pending_work_binding();
