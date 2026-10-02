-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-599: durable workflow requests, not an execution queue or launch grant.
SET LOCAL lock_timeout = '5s';
CREATE TABLE lane_dispatches (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 project_id uuid NOT NULL,
 lane_id uuid,
 ticket_node_id uuid NOT NULL,
 ticket_revision timestamptz NOT NULL CHECK (isfinite(ticket_revision)),
 lane_revision bigint NOT NULL DEFAULT 0 CHECK (lane_revision>=0),
 run_id uuid,
 phase text NOT NULL CHECK (phase IN ('requested','preparation_requested','needs_person','coordinator_requested','completed','cancelled')),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision>0),
 draft_criteria jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(draft_criteria)='array' AND jsonb_array_length(draft_criteria)<=32 AND octet_length(draft_criteria::text)<=16384),
 draft_estimate_hours numeric(10,2) NOT NULL DEFAULT 0 CHECK (draft_estimate_hours BETWEEN 0 AND 200),
 requested_by_principal_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,id),
 FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,project_id,lane_id) REFERENCES autopilot_lanes(tenant_id,project_id,node_id),
 FOREIGN KEY (tenant_id,ticket_node_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,run_id) REFERENCES agent_runs(tenant_id,id),
 FOREIGN KEY (tenant_id,requested_by_principal_id) REFERENCES principals(tenant_id,id),
 CHECK ((lane_id IS NULL AND lane_revision=0 AND phase IN ('coordinator_requested','completed','cancelled') AND run_id IS NOT NULL)
 OR (lane_id IS NOT NULL AND lane_revision>0 AND phase<>'coordinator_requested'))
);
CREATE UNIQUE INDEX lane_dispatches_one_active_ticket ON lane_dispatches(tenant_id,ticket_node_id)
 WHERE phase NOT IN ('completed','cancelled');
CREATE INDEX lane_dispatches_lane_page ON lane_dispatches(tenant_id,lane_id,id);
ALTER TABLE lane_dispatches ENABLE ROW LEVEL SECURITY;
ALTER TABLE lane_dispatches FORCE ROW LEVEL SECURITY;
CREATE POLICY lane_dispatches_tenant ON lane_dispatches
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY lane_dispatches_project ON lane_dispatches AS RESTRICTIVE
 USING ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));
CREATE FUNCTION aeon_lane_dispatch_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.tenant_id=NEW.tenant_id AND n.id=NEW.ticket_node_id AND n.project_id=NEW.project_id
 AND n.deleted_at IS NULL AND k.slug IN ('ticket','task')) THEN
  RAISE EXCEPTION 'dispatch ticket must belong to project' USING ERRCODE='23514';
 END IF;
 IF NEW.run_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM agent_runs r JOIN nodes o ON o.tenant_id=r.tenant_id AND o.id=r.work_order_id
 WHERE r.tenant_id=NEW.tenant_id AND r.id=NEW.run_id AND o.project_id=NEW.project_id
 AND (r.queue_node_id=NEW.ticket_node_id OR o.parent_id=NEW.ticket_node_id)) THEN
  RAISE EXCEPTION 'dispatch run must belong to ticket' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER lane_dispatches_guard BEFORE INSERT OR UPDATE ON lane_dispatches
 FOR EACH ROW EXECUTE FUNCTION aeon_lane_dispatch_guard();
