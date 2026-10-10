-- SPDX-License-Identifier: AGPL-3.0-only
-- aeon:contract-phase AEON-1103 expanded-in=v261005070923.0.0 expansion-migration=1231_work_leaf_binding_guards.sql
-- Reserved 1330. Replace only released busy/cancel functions; no stored data,
-- writer ownership, leases, reservations or confirmed-exit evidence changes.
SET LOCAL lock_timeout = '5s';

-- Two hours from the later of uncertain closure and last heartbeat. NULL
-- heartbeat still gets the full grace period; unknown reasons fail closed.
-- Explicit observation time lets tests prove both sides of the boundary.
-- aeon_work_session_stopped remains IMMUTABLE and means confirmed exit only:
-- binding guards, lead succession, writer admission, escalation and confirm-exit
-- MUST retain it. Busy-hold expiry is never process-exit evidence.
CREATE FUNCTION aeon_work_session_released(p_stopped timestamptz,p_reason text,
 p_heartbeat timestamptz,p_now timestamptz DEFAULT now())
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT aeon_work_session_stopped(p_stopped,p_reason) OR
  (p_stopped IS NOT NULL AND coalesce(p_reason IN
   ('heartbeat_lost','archived_process_unknown','removed_process_unknown',
    'attach detached; process exit unconfirmed'),false)
   AND greatest(p_stopped,p_heartbeat)<=p_now-interval '2 hours');
$$;

-- Only the person's graceful lifecycle action may settle an orphaned run.
-- A bound live/unconfirmed generation always blocks. Recent telemetry blocks
-- even after an old closure; confirmed/released sessions with no later report
-- permit immediate settlement. No generation or daemon proof is fabricated.
CREATE FUNCTION aeon_work_run_releasable(p_run uuid,p_now timestamptz DEFAULT now())
RETURNS boolean LANGUAGE sql STABLE AS $$
 WITH candidate AS (
  SELECT r.*,o.parent_id FROM agent_runs r JOIN nodes o ON o.id=r.work_order_id WHERE r.id=p_run
 ), sessions AS MATERIALIZED (
  SELECT s.stopped_at,s.stop_reason,s.heartbeat_at FROM harness_sessions s JOIN candidate r
   ON s.run_id=r.id OR s.work_order_id=r.work_order_id
    OR s.ticket_node_id IN(r.queue_node_id,r.parent_id,r.work_order_id)
 ) SELECT coalesce((SELECT r.status IN ('starting','running','waiting')
  AND NOT EXISTS(SELECT 1 FROM sessions s
   WHERE NOT aeon_work_session_released(s.stopped_at,s.stop_reason,s.heartbeat_at,p_now))
  AND (greatest(r.created_at,r.started_at,
    (SELECT t.at FROM run_telemetry t WHERE t.run_id=r.id ORDER BY t.sequence DESC LIMIT 1))<=p_now-interval '2 hours'
   OR (EXISTS(SELECT 1 FROM sessions)
    AND NOT EXISTS(SELECT 1 FROM run_telemetry t WHERE t.run_id=r.id
     AND t.at>(SELECT max(s.stopped_at) FROM sessions s))))
 FROM candidate r),false);
$$;

CREATE OR REPLACE FUNCTION aeon_work_busy(p_node uuid) RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE answer boolean; v text := current_setting('aeon.visible_projects',true); s text := current_setting('aeon.system',true);
BEGIN
 PERFORM set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true);
 SELECT EXISTS(SELECT 1 FROM harness_sessions s WHERE (s.ticket_node_id=p_node
   OR s.ticket_node_id IN(SELECT id FROM nodes WHERE parent_id=p_node)
   OR s.work_order_id IN(SELECT id FROM nodes WHERE parent_id=p_node)
   OR s.run_id IN(SELECT r.id FROM agent_runs r JOIN nodes o ON o.id=r.work_order_id WHERE r.queue_node_id=p_node OR o.parent_id=p_node))
   AND NOT aeon_work_session_released(s.stopped_at,s.stop_reason,s.heartbeat_at))
  OR EXISTS(SELECT 1 FROM agent_runs r LEFT JOIN nodes o ON o.id=r.work_order_id
   WHERE (r.queue_node_id=p_node OR o.parent_id=p_node) AND r.status IN ('queued','starting','running','waiting'))
  OR EXISTS(SELECT 1 FROM work_orders w JOIN nodes n ON n.id=w.node_id WHERE n.parent_id=p_node AND n.deleted_at IS NULL AND w.status='running') INTO answer;
 PERFORM set_config('aeon.visible_projects',coalesce(v,''),true),set_config('aeon.system',coalesce(s,''),true);
 RETURN answer;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.visible_projects',coalesce(v,''),true),set_config('aeon.system',coalesce(s,''),true); RAISE;
END;
$$;

-- Invoker RLS only: never widen visibility to describe holders. No titles,
-- hostnames, principal names, notes, telemetry payloads or lease data leave SQL.
-- Fetch at most 21 of each type and show 20, with explicit truncation.
CREATE FUNCTION aeon_work_busy_holders(p_node uuid) RETURNS text LANGUAGE sql STABLE AS $$
 WITH sessions AS MATERIALIZED (
  SELECT 'session' AS kind,s.id,left(regexp_replace(coalesce(nullif(s.display_label,''),s.harness),'[[:cntrl:]]',' ','g'),64) AS label,
   greatest(s.stopped_at,s.heartbeat_at,s.created_at) AS since
  FROM harness_sessions s WHERE (s.ticket_node_id=p_node
   OR s.ticket_node_id IN(SELECT id FROM nodes WHERE parent_id=p_node)
   OR s.work_order_id IN(SELECT id FROM nodes WHERE parent_id=p_node)
   OR s.run_id IN(SELECT r.id FROM agent_runs r JOIN nodes o ON o.id=r.work_order_id WHERE r.queue_node_id=p_node OR o.parent_id=p_node))
   AND NOT aeon_work_session_released(s.stopped_at,s.stop_reason,s.heartbeat_at)
  ORDER BY s.id LIMIT 21
 ), runs AS MATERIALIZED (
  SELECT 'run' AS kind,r.id,'agent run' AS label,r.created_at AS since
  FROM agent_runs r JOIN nodes o ON o.id=r.work_order_id
  WHERE (r.queue_node_id=p_node OR o.parent_id=p_node) AND r.status IN ('queued','starting','running','waiting')
  ORDER BY r.id LIMIT 21
 ), orders AS MATERIALIZED (
  SELECT 'work order' AS kind,w.node_id AS id,'work order' AS label,w.updated_at AS since
  FROM work_orders w JOIN nodes n ON n.id=w.node_id
  WHERE n.parent_id=p_node AND n.deleted_at IS NULL AND w.status='running'
  ORDER BY w.node_id LIMIT 21
 ), holders AS (
  SELECT * FROM (SELECT * FROM sessions LIMIT 20) s
  UNION ALL SELECT * FROM (SELECT * FROM runs LIMIT 20) r
  UNION ALL SELECT * FROM (SELECT * FROM orders LIMIT 20) o
 ) SELECT coalesce(string_agg(format('%s %s (%s; age %s seconds)',kind,id,label,
  greatest(0,floor(extract(epoch FROM now()-since)))::bigint),'; ' ORDER BY kind,id),'holder outside visible scope')
  || CASE WHEN (SELECT count(*) FROM sessions)>20 OR (SELECT count(*) FROM runs)>20 OR (SELECT count(*) FROM orders)>20
   THEN '; additional holders omitted' ELSE '' END FROM holders;
$$;

CREATE OR REPLACE FUNCTION aeon_guard_busy_work_cancel() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state IS DISTINCT FROM OLD.state AND NEW.deleted_at IS NULL
  AND EXISTS(SELECT 1 FROM node_kinds k WHERE k.id=NEW.kind_id AND k.slug='work'
   AND aeon_work_status_category(NEW.state,k.field_schema)='cancelled')
  AND aeon_work_busy(NEW.id) THEN
  RAISE EXCEPTION 'request graceful stop before cancelling busy work: %',aeon_work_busy_holders(NEW.id)
   USING ERRCODE='23514',CONSTRAINT='busy_work_leaf';
 END IF;
 RETURN NEW;
END;
$$;
