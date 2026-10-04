-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-652, reserved by the package brief. Historical bindings are untouched.
SET LOCAL lock_timeout = '5s';

-- Canonical predicates see hidden work children, but reveal only a boolean.
-- Restore both visibility settings even when a query fails.
CREATE FUNCTION aeon_work_leaf(p_node uuid) RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE answer boolean; v text := current_setting('aeon.visible_projects',true); s text := current_setting('aeon.system',true);
BEGIN
 PERFORM set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true);
 SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k USING(tenant_id)
  WHERE n.id=p_node AND k.id=n.kind_id AND n.deleted_at IS NULL AND k.slug='work'
   AND NOT EXISTS(SELECT 1 FROM nodes c JOIN node_kinds ck USING(tenant_id)
    WHERE c.parent_id=n.id AND c.deleted_at IS NULL AND ck.id=c.kind_id AND ck.slug='work')) INTO answer;
 PERFORM set_config('aeon.visible_projects',coalesce(v,''),true),set_config('aeon.system',coalesce(s,''),true);
 RETURN answer;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.visible_projects',coalesce(v,''),true),set_config('aeon.system',coalesce(s,''),true); RAISE;
END;
$$;

-- Silence, lost ownership and administrative removal cannot prove process exit.
-- Unknown reasons fail closed. All work guards share this explicit predicate.
CREATE FUNCTION aeon_work_session_stopped(p_stopped timestamptz,p_reason text)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT p_stopped IS NOT NULL AND coalesce(p_reason IN
  ('stopped','completed','process_exited','process_failed','force_stopped',
   'token_budget_exhausted','turn_budget_exhausted','paused',
   'attach confirmed exited by kernel process check'),false);
$$;

CREATE FUNCTION aeon_work_busy(p_node uuid) RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE answer boolean; v text := current_setting('aeon.visible_projects',true); s text := current_setting('aeon.system',true);
BEGIN
 PERFORM set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true);
 SELECT EXISTS(SELECT 1 FROM harness_sessions s WHERE (s.ticket_node_id=p_node
   OR s.ticket_node_id IN(SELECT id FROM nodes WHERE parent_id=p_node)
   OR s.work_order_id IN(SELECT id FROM nodes WHERE parent_id=p_node)
   OR s.run_id IN(SELECT r.id FROM agent_runs r JOIN nodes o ON o.id=r.work_order_id WHERE r.queue_node_id=p_node OR o.parent_id=p_node)) AND NOT aeon_work_session_stopped(s.stopped_at,s.stop_reason))
  OR EXISTS(SELECT 1 FROM agent_runs r LEFT JOIN nodes o ON o.id=r.work_order_id
   WHERE (r.queue_node_id=p_node OR o.parent_id=p_node) AND r.status IN ('queued','starting','running','waiting'))
  OR EXISTS(SELECT 1 FROM work_orders w JOIN nodes n ON n.id=w.node_id WHERE n.parent_id=p_node AND n.deleted_at IS NULL AND w.status='running') INTO answer;
 PERFORM set_config('aeon.visible_projects',coalesce(v,''),true),set_config('aeon.system',coalesce(s,''),true);
 RETURN answer;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.visible_projects',coalesce(v,''),true),set_config('aeon.system',coalesce(s,''),true); RAISE;
END;
$$;

CREATE FUNCTION aeon_require_work_leaf(p_node uuid) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.id=p_node AND k.slug='work')
  AND NOT aeon_work_leaf(p_node) THEN
  RAISE EXCEPTION 'agents work on leaves only' USING ERRCODE='23514',CONSTRAINT='work_leaf_required';
 END IF;
END;
$$;

CREATE FUNCTION aeon_guard_work_binding() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target uuid; targets uuid[] := ARRAY[]::uuid[]; reviving boolean:=false;
BEGIN
 -- The endpoint holds pairing -> tree -> tenant before any resource rows.
 -- SQL writers use the same tree advisory key. Same-generation revival and
 -- progress keep the existing binding, even during a pending handover.
 IF TG_TABLE_NAME='harness_sessions' THEN
  IF aeon_work_session_stopped(NEW.stopped_at,NEW.stop_reason) THEN RETURN NEW; END IF;
  IF TG_OP='UPDATE' AND NEW.ticket_node_id IS NOT DISTINCT FROM OLD.ticket_node_id
   AND NEW.work_order_id IS NOT DISTINCT FROM OLD.work_order_id AND NEW.run_id IS NOT DISTINCT FROM OLD.run_id AND NOT aeon_work_session_stopped(OLD.stopped_at,OLD.stop_reason) THEN
   IF OLD.stopped_at IS NOT NULL AND NEW.stopped_at IS NULL THEN reviving:=true;
   ELSE RETURN NEW; END IF;
  END IF;
  targets:=array_append(targets,NEW.ticket_node_id);
  IF NEW.work_order_id IS NOT NULL THEN SELECT array_append(targets,parent_id) INTO targets FROM nodes WHERE id=NEW.work_order_id; END IF;
  IF NEW.run_id IS NOT NULL THEN
   SELECT targets || ARRAY[r.queue_node_id,o.parent_id] INTO targets FROM agent_runs r JOIN nodes o ON o.id=r.work_order_id WHERE r.id=NEW.run_id;
  END IF;
  target:=NEW.ticket_node_id;
  IF target IS NULL AND NEW.work_order_id IS NOT NULL THEN SELECT parent_id INTO target FROM nodes WHERE id=NEW.work_order_id; END IF;
  IF target IS NULL AND NEW.run_id IS NOT NULL THEN
   SELECT coalesce(r.queue_node_id,o.parent_id) INTO target FROM agent_runs r JOIN nodes o ON o.id=r.work_order_id WHERE r.id=NEW.run_id;
  END IF;
 ELSIF TG_TABLE_NAME='agent_runs' THEN
  IF NEW.status NOT IN ('queued','starting','running','waiting') THEN RETURN NEW; END IF;
  IF TG_OP='UPDATE' AND (NEW.status=OLD.status OR OLD.status IN ('starting','running','waiting')) AND NEW.queue_node_id IS NOT DISTINCT FROM OLD.queue_node_id
    AND NEW.work_order_id=OLD.work_order_id THEN RETURN NEW; END IF;
  targets:=array_append(targets,NEW.queue_node_id);
  SELECT array_append(targets,parent_id) INTO targets FROM nodes WHERE id=NEW.work_order_id;
  target:=NEW.queue_node_id;
  IF target IS NULL THEN SELECT parent_id INTO target FROM nodes WHERE id=NEW.work_order_id; END IF;
 ELSE
  IF TG_OP='UPDATE' AND (NEW.status=OLD.status OR NEW.status NOT IN('ready','running')) THEN RETURN NEW; END IF;
  IF NEW.status IN ('done','cancelled') THEN RETURN NEW; END IF;
  SELECT parent_id INTO target FROM nodes WHERE id=NEW.node_id;
 END IF;
 targets:=array_append(targets,target);
 PERFORM pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id'),0));
 FOREACH target IN ARRAY targets LOOP
 -- Work-order session bindings resolve to the work item, while standalone
 -- non-work orders remain supported for pairing verification and coordinators.
 IF EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.id=target AND k.slug='work_order') THEN
  SELECT parent_id INTO target FROM nodes WHERE id=target;
 END IF;
 IF reviving THEN
  -- Recovery may finish this generation's pending handover, but never admit
  -- work on a historical parent that was split before this guard existed.
  IF EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.id=target AND k.slug='work') AND NOT aeon_work_leaf(target) THEN
   RAISE EXCEPTION 'agents work on leaves only' USING ERRCODE='23514',CONSTRAINT='work_leaf_required';
  END IF;
 ELSE PERFORM aeon_require_work_leaf(target); END IF;
 END LOOP;
 RETURN NEW;
END;
$$;
CREATE TRIGGER harness_work_leaf BEFORE INSERT OR UPDATE OF ticket_node_id,work_order_id,run_id,stopped_at ON harness_sessions FOR EACH ROW EXECUTE FUNCTION aeon_guard_work_binding();
CREATE TRIGGER runs_work_leaf BEFORE INSERT OR UPDATE OF status,queue_node_id,work_order_id ON agent_runs FOR EACH ROW EXECUTE FUNCTION aeon_guard_work_binding();
CREATE TRIGGER orders_work_leaf BEFORE INSERT OR UPDATE OF status ON work_orders FOR EACH ROW EXECUTE FUNCTION aeon_guard_work_binding();

CREATE FUNCTION aeon_guard_busy_work_child() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE slug text;
BEGIN
 IF NEW.deleted_at IS NOT NULL THEN RETURN NEW; END IF;
 IF TG_OP='UPDATE' AND NEW.parent_id IS NOT DISTINCT FROM OLD.parent_id AND NEW.kind_id=OLD.kind_id AND OLD.deleted_at IS NULL THEN RETURN NEW; END IF;
 SELECT k.slug INTO slug FROM node_kinds k WHERE k.id=NEW.kind_id;
 IF slug NOT IN ('work','work_order') OR NEW.parent_id IS NULL THEN RETURN NEW; END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id'),0));
 IF slug='work_order' THEN
  PERFORM aeon_require_work_leaf(NEW.parent_id);
 ELSIF aeon_work_busy(NEW.parent_id) THEN
  RAISE EXCEPTION 'busy leaf requires graceful handover before adding work children' USING ERRCODE='23514',CONSTRAINT='busy_work_leaf';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER nodes_busy_work_child BEFORE INSERT OR UPDATE OF parent_id,kind_id,deleted_at ON nodes FOR EACH ROW EXECUTE FUNCTION aeon_guard_busy_work_child();

-- Ordinary cancel writes cannot mark an active leaf cancelled behind its run.
CREATE FUNCTION aeon_guard_busy_work_cancel() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state IS DISTINCT FROM OLD.state AND NEW.deleted_at IS NULL
  AND EXISTS(SELECT 1 FROM node_kinds k WHERE k.id=NEW.kind_id AND k.slug='work'
   AND aeon_work_status_category(NEW.state,k.field_schema)='cancelled')
  AND aeon_work_busy(NEW.id) THEN
  RAISE EXCEPTION 'request graceful stop before cancelling busy work' USING ERRCODE='23514',CONSTRAINT='busy_work_leaf';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER nodes_busy_work_cancel BEFORE UPDATE OF state ON nodes FOR EACH ROW EXECUTE FUNCTION aeon_guard_busy_work_cancel();
