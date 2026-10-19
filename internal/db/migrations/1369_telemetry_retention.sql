-- SPDX-License-Identifier: AGPL-3.0-only
-- aeon:contract-phase AEON-1354 expanded-in=v261017115202.0.0 expansion-migration=0101_relations_events.sql
-- AEON-1354. No cleanup runs in the migration. The release backs up before
-- starting the server's bounded retention worker. All other events stay immutable.
SET LOCAL lock_timeout = '5s';

CREATE POLICY events_telemetry_retention ON events FOR DELETE USING (
 tenant_id = nullif(current_setting('aeon.tenant_id',true),'')::uuid
 AND current_setting('aeon.system',true) = 'on'
 AND current_setting('aeon.telemetry_prune',true) = 'on'
 AND aeon_visible_all()
 AND type IN ('account.probed','harness.heartbeat')
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
   AND OLD.type IN ('account.probed','harness.heartbeat')
   AND OLD.at < statement_timestamp() - interval '14 days' AND OLD.undo_of IS NULL
   AND NOT EXISTS(SELECT 1 FROM public.events u WHERE u.tenant_id=OLD.tenant_id AND u.undo_of=OLD.id)
  THEN RETURN OLD; END IF;
 END IF;
 RAISE EXCEPTION 'events are append-only';
END;
$$;

-- Invoker rights retain FORCE RLS. Only an explicit tenant service transaction
-- can enter this function; the flag is automatically restored on function exit.
-- The minimum age is also enforced by the policy and immutable-row trigger.
CREATE FUNCTION aeon_prune_telemetry(target_tenant uuid, cutoff timestamptz, batch_size integer)
RETURNS TABLE(event_type text, deleted bigint, undo_protected boolean)
LANGUAGE plpgsql SECURITY INVOKER
SET search_path = pg_catalog, public, pg_temp
SET aeon.telemetry_prune = 'on'
AS $$
DECLARE protected boolean;
BEGIN
 IF target_tenant IS DISTINCT FROM nullif(current_setting('aeon.tenant_id',true),'')::uuid
  OR current_setting('aeon.system',true) IS DISTINCT FROM 'on' OR NOT public.aeon_visible_all()
 THEN RAISE EXCEPTION 'telemetry retention requires the current tenant service context' USING ERRCODE='42501'; END IF;
 IF cutoff IS NULL OR cutoff > statement_timestamp()-interval '14 days'
  OR batch_size IS NULL OR batch_size < 1 OR batch_size > 500
 THEN RAISE EXCEPTION 'invalid telemetry retention bounds' USING ERRCODE='22023'; END IF;
 IF NOT pg_try_advisory_xact_lock(hashtextextended('telemetry-retention:'||target_tenant::text,1354)) THEN
  RETURN; -- Another replica owns this batch.
 END IF;
 SELECT EXISTS(SELECT 1 FROM public.events e WHERE e.tenant_id=target_tenant
  AND e.type IN ('account.probed','harness.heartbeat') AND e.at<cutoff
  AND (e.undo_of IS NOT NULL OR EXISTS(SELECT 1 FROM public.events u WHERE u.tenant_id=e.tenant_id AND u.undo_of=e.id))) INTO protected;
 IF protected THEN
  RETURN QUERY SELECT t,0::bigint,true FROM unnest(ARRAY['account.probed','harness.heartbeat']) t;
  RETURN;
 END IF;
 RETURN QUERY WITH candidates AS MATERIALIZED (
  SELECT e.tenant_id,e.id FROM public.events e WHERE e.tenant_id=target_tenant
   AND e.type IN ('account.probed','harness.heartbeat') AND e.at<cutoff AND e.undo_of IS NULL
   ORDER BY e.at,e.id LIMIT batch_size FOR UPDATE SKIP LOCKED
 ), removed AS (
  DELETE FROM public.events e USING candidates c WHERE e.tenant_id=c.tenant_id AND e.id=c.id
  RETURNING e.type
 ) SELECT t,count(r.type),false FROM unnest(ARRAY['account.probed','harness.heartbeat']) t
  LEFT JOIN removed r ON r.type=t GROUP BY t;
END;
$$;
