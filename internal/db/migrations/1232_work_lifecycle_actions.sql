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
 result jsonb NOT NULL DEFAULT '[]',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 completed_at timestamptz,
 PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,node_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY(tenant_id,requested_by) REFERENCES principals(tenant_id,id)
);
ALTER TABLE work_lifecycle_actions ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_lifecycle_actions FORCE ROW LEVEL SECURITY;
CREATE POLICY work_lifecycle_tenant ON work_lifecycle_actions USING (
 tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid AND EXISTS (
 SELECT 1 FROM nodes n WHERE n.id=node_id AND n.tenant_id=work_lifecycle_actions.tenant_id));
CREATE INDEX work_lifecycle_waiting_tenant ON work_lifecycle_actions(tenant_id) WHERE state='waiting';
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
