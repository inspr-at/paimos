-- SPDX-License-Identifier: AGPL-3.0-only
-- aeon:contract-phase AEON-1354 expanded-in=v261017115202.0.0 expansion-migration=0101_relations_events.sql
-- AEON-1354. No cleanup runs in the migration. The release backs up before
-- starting the server's bounded retention worker. All other events stay immutable.
SET LOCAL lock_timeout = '5s';

-- The one prune rule, shared by the policy, the trigger, the batch selection
-- and the partial index (1370). Account probes are telemetry. A heartbeat is a
-- quiet tick only when it left every fact goal history replays unchanged
-- (internal/goals/series.go): session, phase, role, bound ticket, stop and
-- archive. Phase changes, rebinds and stops stay in the log for good. The
-- SQL-standard body binds its operators now, independent of search_path.
CREATE FUNCTION aeon_telemetry_prunable(event_type text, event_before jsonb, event_after jsonb)
RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
RETURN event_type = 'account.probed'
 OR (event_type = 'harness.heartbeat' AND event_before IS NOT NULL AND event_after IS NOT NULL
  AND event_before->'id' IS NOT DISTINCT FROM event_after->'id'
  AND event_before->'phase' IS NOT DISTINCT FROM event_after->'phase'
  AND event_before->'role' IS NOT DISTINCT FROM event_after->'role'
  AND event_before->'ticket_node_id' IS NOT DISTINCT FROM event_after->'ticket_node_id'
  AND event_before->'stopped_at' IS NOT DISTINCT FROM event_after->'stopped_at'
  AND event_before->'stop_reason' IS NOT DISTINCT FROM event_after->'stop_reason'
  AND event_before->'archived_at' IS NOT DISTINCT FROM event_after->'archived_at');

CREATE POLICY events_telemetry_retention ON events FOR DELETE USING (
 tenant_id = nullif(current_setting('aeon.tenant_id',true),'')::uuid
 AND current_setting('aeon.system',true) = 'on'
 AND current_setting('aeon.telemetry_prune',true) = 'on'
 AND aeon_visible_all()
 AND aeon_telemetry_prunable(type,before,after)
 AND at < statement_timestamp() - interval '14 days'
 AND undo_of IS NULL
);

CREATE OR REPLACE FUNCTION aeon_events_append_only() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
 IF TG_OP='DELETE' AND TG_TABLE_SCHEMA='public' AND TG_TABLE_NAME='events' THEN
  IF current_setting('aeon.telemetry_prune',true)='on'
   AND current_setting('aeon.system',true)='on' AND public.aeon_visible_all()
   AND OLD.tenant_id = nullif(current_setting('aeon.tenant_id',true),'')::uuid
   AND public.aeon_telemetry_prunable(OLD.type,OLD.before,OLD.after)
   AND OLD.at < statement_timestamp() - interval '14 days' AND OLD.undo_of IS NULL
   AND NOT EXISTS(SELECT 1 FROM public.events u WHERE u.tenant_id=OLD.tenant_id AND u.undo_of=OLD.id)
  THEN RETURN OLD; END IF;
 END IF;
 RAISE EXCEPTION 'events are append-only';
END;
$$;

-- Invoker rights retain FORCE RLS. Only an explicit tenant service transaction
-- can enter this function. The prune flag is set for the body and restored
-- before return, including on error. A NOSUPERUSER migration role cannot store
-- this placeholder in the function configuration (SQLSTATE 42501). The minimum
-- age is also enforced by the policy and immutable-row trigger.
CREATE FUNCTION aeon_prune_telemetry(target_tenant uuid, cutoff timestamptz, batch_size integer)
RETURNS TABLE(event_type text, deleted bigint, undo_protected boolean)
LANGUAGE plpgsql SECURITY INVOKER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
 protected boolean;
 batch_ids bigint[];
 prior_prune text := current_setting('aeon.telemetry_prune',true);
BEGIN
 PERFORM set_config('aeon.telemetry_prune','on',true);
 IF target_tenant IS DISTINCT FROM nullif(current_setting('aeon.tenant_id',true),'')::uuid
  OR current_setting('aeon.system',true) IS DISTINCT FROM 'on' OR NOT public.aeon_visible_all()
 THEN RAISE EXCEPTION 'telemetry retention requires the current tenant service context' USING ERRCODE='42501'; END IF;
 IF cutoff IS NULL OR cutoff > statement_timestamp()-interval '14 days'
  OR batch_size IS NULL OR batch_size < 1 OR batch_size > 500
 THEN RAISE EXCEPTION 'invalid telemetry retention bounds' USING ERRCODE='22023'; END IF;
 IF pg_try_advisory_xact_lock(hashtextextended('telemetry-retention:'||target_tenant::text,1354)) THEN
  -- Start at the indexed, normally empty undo set. Scanning every old tick
  -- for each batch would turn the initial million-row cleanup quadratic.
  SELECT EXISTS(SELECT 1 FROM public.events u LEFT JOIN public.events e
   ON e.tenant_id=u.tenant_id AND e.id=u.undo_of
   WHERE u.tenant_id=target_tenant AND u.undo_of IS NOT NULL
   AND ((public.aeon_telemetry_prunable(u.type,u.before,u.after) AND u.at<cutoff)
    OR (public.aeon_telemetry_prunable(e.type,e.before,e.after) AND e.at<cutoff))) INTO protected;
  IF protected THEN
   RETURN QUERY SELECT t,0::bigint,true FROM unnest(ARRAY['account.probed','harness.heartbeat']) t;
  ELSE
   -- No locking clause: SELECT FOR UPDATE applies UPDATE policies, and events
   -- has none, so it matched nothing. The advisory lock serialises pruners;
   -- the DELETE locks its own rows and the trigger rechecks each one.
   SELECT coalesce(array_agg(c.id ORDER BY c.at,c.id),'{}') INTO batch_ids FROM (
    SELECT e.id,e.at FROM public.events e WHERE e.tenant_id=target_tenant
     AND public.aeon_telemetry_prunable(e.type,e.before,e.after) AND e.at<cutoff AND e.undo_of IS NULL
     ORDER BY e.at,e.id LIMIT batch_size
   ) c;
   -- Goal history references go first: their read policy hides a reference
   -- once its event is gone, and a later delete could never reach it. 1361
   -- derives every reference from the immutable node_refs and node_id, so the
   -- primary key finds them without scanning the tenant's references.
   DELETE FROM public.goal_event_refs r USING public.events e
    CROSS JOIN LATERAL unnest(e.node_refs||ARRAY[e.node_id]) ref(node_id)
    WHERE e.tenant_id=target_tenant AND e.id=ANY(batch_ids)
     AND r.tenant_id=target_tenant AND r.node_id=ref.node_id AND r.event_id=e.id;
   RETURN QUERY WITH removed AS (
    DELETE FROM public.events e WHERE e.tenant_id=target_tenant AND e.id=ANY(batch_ids)
    RETURNING e.type
   ) SELECT t,count(r.type),false FROM unnest(ARRAY['account.probed','harness.heartbeat']) t
    LEFT JOIN removed r ON r.type=t GROUP BY t;
  END IF;
 END IF; -- Another replica owns this batch: return no rows after restoring the flag.
 PERFORM set_config('aeon.telemetry_prune',coalesce(prior_prune,''),true);
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.telemetry_prune',coalesce(prior_prune,''),true);
 RAISE;
END;
$$;
