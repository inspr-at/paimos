-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-600. Window identity excludes policy revision: editing a lane cannot
-- reset a period's held or settled usage. Amounts are integer milliseconds.
SET LOCAL lock_timeout = '5s';
CREATE TABLE lane_budget_periods (
 tenant_id uuid NOT NULL,
 lane_id uuid NOT NULL,
 project_id uuid NOT NULL,
 starts_at timestamptz NOT NULL,
 ends_at timestamptz NOT NULL,
 limit_ms bigint NOT NULL CHECK (limit_ms BETWEEN 1 AND 36000000000),
 settled_ms bigint NOT NULL DEFAULT 0 CHECK (settled_ms>=0),
 held_ms bigint NOT NULL DEFAULT 0 CHECK (held_ms>=0),
 PRIMARY KEY (tenant_id,lane_id,starts_at),
 UNIQUE (tenant_id,project_id,lane_id,starts_at),
 FOREIGN KEY (tenant_id,project_id,lane_id) REFERENCES autopilot_lanes(tenant_id,project_id,node_id),
 CHECK (isfinite(starts_at) AND isfinite(ends_at) AND ends_at>starts_at),
 CHECK (settled_ms+held_ms<=limit_ms)
);
CREATE TABLE lane_budget_envelopes (
 tenant_id uuid NOT NULL,
 id uuid NOT NULL,
 project_id uuid NOT NULL,
 lane_id uuid NOT NULL,
 period_start timestamptz NOT NULL,
 ticket_node_id uuid NOT NULL,
 owner_principal_id uuid NOT NULL,
 lane_revision bigint NOT NULL CHECK (lane_revision>0),
 maximum_ms bigint NOT NULL CHECK (maximum_ms BETWEEN 1 AND 36000000000),
 attempt_ms bigint NOT NULL CHECK (attempt_ms>2000 AND attempt_ms<=maximum_ms),
 settled_ms bigint NOT NULL DEFAULT 0 CHECK (settled_ms>=0 AND settled_ms<=maximum_ms),
 closed boolean NOT NULL DEFAULT false,
 PRIMARY KEY (tenant_id,id),
 UNIQUE (tenant_id,project_id,id),
 FOREIGN KEY (tenant_id,project_id,lane_id,period_start) REFERENCES lane_budget_periods(tenant_id,project_id,lane_id,starts_at),
 FOREIGN KEY (tenant_id,ticket_node_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,owner_principal_id) REFERENCES principals(tenant_id,id)
);
CREATE UNIQUE INDEX lane_one_ticket_envelope ON lane_budget_envelopes(tenant_id,ticket_node_id) WHERE NOT closed;
ALTER TABLE agent_runs ADD COLUMN lane_envelope_id uuid,
 ADD CONSTRAINT agent_run_lane_envelope FOREIGN KEY (tenant_id,lane_envelope_id) REFERENCES lane_budget_envelopes(tenant_id,id);
CREATE INDEX agent_runs_lane ON agent_runs(tenant_id,lane_envelope_id) WHERE lane_envelope_id IS NOT NULL;
CREATE TABLE lane_attempt_grants (
 tenant_id uuid NOT NULL,
 run_id uuid NOT NULL,
 project_id uuid NOT NULL,
 envelope_id uuid NOT NULL,
 workspace_id uuid NOT NULL,
 daemon_id text NOT NULL CHECK (octet_length(daemon_id) BETWEEN 1 AND 128),
 daemon_generation text NOT NULL CHECK (octet_length(daemon_generation) BETWEEN 1 AND 128),
 maximum_ms bigint NOT NULL CHECK (maximum_ms>2000 AND maximum_ms<=36000000000),
 expires_at timestamptz NOT NULL CHECK (isfinite(expires_at)),
 elapsed_ms bigint CHECK (elapsed_ms>=0 AND elapsed_ms<=maximum_ms),
 PRIMARY KEY (tenant_id,run_id),
 UNIQUE (tenant_id,workspace_id),
 FOREIGN KEY (tenant_id,run_id) REFERENCES agent_runs(tenant_id,id),
 FOREIGN KEY (tenant_id,project_id,envelope_id) REFERENCES lane_budget_envelopes(tenant_id,project_id,id)
);
-- A dispatch has one owned process at a time. Unknown exit is an open grant,
-- even when legacy run telemetry or cancellation has marked the run terminal.
CREATE UNIQUE INDEX lane_one_open_attempt ON lane_attempt_grants(tenant_id,envelope_id) WHERE elapsed_ms IS NULL;
DO $$ DECLARE n text; BEGIN
 FOREACH n IN ARRAY ARRAY['lane_budget_periods','lane_budget_envelopes','lane_attempt_grants'] LOOP
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',n);
  EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',n);
  EXECUTE format('CREATE POLICY tenant ON %I USING (tenant_id=NULLIF(current_setting(''aeon.tenant_id'',true),'''')::uuid) WITH CHECK (tenant_id=NULLIF(current_setting(''aeon.tenant_id'',true),'''')::uuid)',n);
  EXECUTE format('CREATE POLICY project ON %I AS RESTRICTIVE USING ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[])) WITH CHECK ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))',n);
 END LOOP;
END $$;
-- This narrow aggregate sees tenant-wide slots, including other projects, but
-- exposes no identifiers or content. Function-local GUC is restored on return.
CREATE FUNCTION aeon_lane_working_slots(owner_id uuid, exclude_run uuid)
RETURNS TABLE(area text,harness text) LANGUAGE plpgsql AS $$
DECLARE prior text := current_setting('aeon.visible_projects',true);
BEGIN
 PERFORM set_config('aeon.visible_projects','*',true);
 RETURN QUERY WITH occupied AS (
  SELECT r.id,coalesce(ticket.fields->>'area','') AS area,coalesce(m.harness,'') AS harness
  FROM agent_runs r JOIN work_orders w ON w.tenant_id=r.tenant_id AND w.node_id=r.work_order_id
  JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_order_id
  LEFT JOIN nodes ticket ON ticket.tenant_id=n.tenant_id AND ticket.id=n.parent_id
  LEFT JOIN model_profiles m ON m.tenant_id=r.tenant_id AND m.id=r.model_profile_id
  LEFT JOIN lane_budget_envelopes e ON e.tenant_id=r.tenant_id AND e.id=r.lane_envelope_id
  LEFT JOIN agent_accounts a ON a.tenant_id=r.tenant_id AND a.id=r.account_id
  LEFT JOIN principals requester ON requester.tenant_id=w.tenant_id AND requester.id=w.requested_by_principal_id AND requester.kind='person'
  WHERE r.id<>exclude_run
   AND (coalesce(e.owner_principal_id,a.capacity_owner,requester.id) IS NULL OR coalesce(e.owner_principal_id,a.capacity_owner,requester.id)=owner_id)
   AND (r.status IN ('starting','running','waiting','ownership_lost')
    OR EXISTS(SELECT 1 FROM lane_attempt_grants g WHERE g.run_id=r.id AND g.elapsed_ms IS NULL)
    OR (r.status='queued' AND r.lane_envelope_id IS NULL AND EXISTS(SELECT 1 FROM account_reservations ar WHERE ar.run_id=r.id AND ar.state='active')))
 ), sessions AS (
  SELECT coalesce(n.fields->>'area','') AS area,s.harness
  FROM harness_sessions s LEFT JOIN nodes n ON n.tenant_id=s.tenant_id AND n.id=s.ticket_node_id
  WHERE (s.owner_principal_id IS NULL OR s.owner_principal_id=owner_id)
   AND (s.stopped_at IS NULL OR s.stop_reason='ownership_lost')
   AND s.run_id IS DISTINCT FROM exclude_run
   -- A managed process is counted by its run/grant, even during a stale session
   -- heartbeat. A confirmed finished process waiting for a person takes no slot.
   AND NOT EXISTS(SELECT 1 FROM agent_runs r WHERE r.id=s.run_id)
 ) SELECT o.area,o.harness FROM occupied o UNION ALL SELECT s.area,s.harness FROM sessions s LIMIT 13;
 PERFORM set_config('aeon.visible_projects',coalesce(prior,''),true);
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.visible_projects',coalesce(prior,''),true);
 RAISE;
END $$;

CREATE FUNCTION aeon_lane_run_binding_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND OLD.lane_envelope_id IS NOT NULL AND
  (NEW.lane_envelope_id IS DISTINCT FROM OLD.lane_envelope_id OR NEW.work_order_id<>OLD.work_order_id) THEN
  RAISE EXCEPTION 'lane run binding is immutable' USING ERRCODE='23514';
 END IF;
 IF NEW.lane_envelope_id IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM lane_budget_envelopes e JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=NEW.work_order_id
  WHERE e.tenant_id=NEW.tenant_id AND e.id=NEW.lane_envelope_id AND n.parent_id=e.ticket_node_id AND n.project_id=e.project_id
 ) THEN RAISE EXCEPTION 'lane run project mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER lane_run_binding_guard BEFORE INSERT OR UPDATE OF lane_envelope_id,work_order_id ON agent_runs
 FOR EACH ROW EXECUTE FUNCTION aeon_lane_run_binding_guard();
