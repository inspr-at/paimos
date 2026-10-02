-- SPDX-License-Identifier: AGPL-3.0-only
-- Policy only: no scheduler, execution queue, launch grant or budget holds.
SET LOCAL lock_timeout = '5s';
CREATE TABLE autopilot_lanes (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 node_id uuid NOT NULL,
 project_id uuid NOT NULL,
 owner_principal_id uuid NOT NULL,
 name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 120 AND length(btrim(name))>0),
 priority integer NOT NULL DEFAULT 100 CHECK (priority BETWEEN 0 AND 1000),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision>0),
 enabled boolean NOT NULL DEFAULT false,
 paused boolean NOT NULL DEFAULT false,
 pause_reason text NOT NULL DEFAULT '' CHECK (octet_length(pause_reason)<=500),
 scope_kind text NOT NULL DEFAULT 'queued_tickets' CHECK (scope_kind IN ('queued_tickets','release')),
 scope_node_id uuid,
 policy jsonb NOT NULL CHECK (jsonb_typeof(policy)='object' AND octet_length(policy::text)<=16384),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id,node_id),
 UNIQUE (tenant_id,project_id,node_id),
 FOREIGN KEY (tenant_id,node_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,owner_principal_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,scope_node_id) REFERENCES nodes(tenant_id,id),
 CHECK ((scope_kind='queued_tickets' AND scope_node_id IS NULL) OR (scope_kind='release' AND scope_node_id IS NOT NULL))
);
CREATE INDEX autopilot_lanes_project_page ON autopilot_lanes(tenant_id,project_id,node_id);
ALTER TABLE autopilot_lanes ENABLE ROW LEVEL SECURITY;
ALTER TABLE autopilot_lanes FORCE ROW LEVEL SECURITY;
CREATE POLICY autopilot_lanes_tenant ON autopilot_lanes
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY autopilot_lanes_project ON autopilot_lanes AS RESTRICTIVE
 USING ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));

-- A preparation intent is not dispatch ownership. AEON-599 must recheck scope,
-- permissions, revisions and eligibility, then obtain ownership and holds before
-- converting it to a dispatch. No worker may consume it as a launch grant.
CREATE TABLE autopilot_preparation_requests (
 tenant_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 project_id uuid NOT NULL,
 lane_id uuid NOT NULL,
 ticket_node_id uuid NOT NULL,
 ticket_revision bigint NOT NULL CHECK (ticket_revision>0),
 lane_revision bigint NOT NULL CHECK (lane_revision>0),
 requested_by_principal_id uuid NOT NULL,
 preparation text NOT NULL CHECK (preparation IN ('automatic_estimate','criteria_draft_requires_person')),
 status text NOT NULL DEFAULT 'requested' CHECK (status='requested'),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id,id),
 UNIQUE (tenant_id,lane_id,ticket_node_id,ticket_revision,lane_revision),
 FOREIGN KEY (tenant_id,project_id,lane_id) REFERENCES autopilot_lanes(tenant_id,project_id,node_id),
 FOREIGN KEY (tenant_id,ticket_node_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,requested_by_principal_id) REFERENCES principals(tenant_id,id)
);
ALTER TABLE autopilot_preparation_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE autopilot_preparation_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY autopilot_preparation_requests_tenant ON autopilot_preparation_requests
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY autopilot_preparation_requests_project ON autopilot_preparation_requests AS RESTRICTIVE
 USING ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));

-- Composite tenant keys alone do not prove kind, project or person ownership.
-- This new-table trigger protects direct projection writers as well as the API.
CREATE FUNCTION aeon_autopilot_lane_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE n.tenant_id=NEW.tenant_id AND n.id=NEW.node_id AND n.project_id=NEW.project_id
  AND n.parent_id=NEW.project_id AND k.slug='autopilot_lane' AND n.deleted_at IS NULL)
 OR NOT EXISTS (SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE n.tenant_id=NEW.tenant_id AND n.id=NEW.project_id AND k.slug='project' AND n.deleted_at IS NULL)
 OR NOT EXISTS (SELECT 1 FROM principals p WHERE p.tenant_id=NEW.tenant_id AND p.id=NEW.owner_principal_id AND p.kind='person') THEN
  RAISE EXCEPTION 'invalid lane project, node or owner' USING ERRCODE='23514';
 END IF;
 IF NEW.scope_kind='release' AND NOT EXISTS (SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE n.tenant_id=NEW.tenant_id AND n.id=NEW.scope_node_id AND n.project_id=NEW.project_id AND k.slug='release' AND n.deleted_at IS NULL) THEN
  RAISE EXCEPTION 'release must belong to lane project' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER autopilot_lanes_guard BEFORE INSERT OR UPDATE ON autopilot_lanes
 FOR EACH ROW EXECUTE FUNCTION aeon_autopilot_lane_guard();
CREATE FUNCTION aeon_autopilot_preparation_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM nodes WHERE tenant_id=NEW.tenant_id AND id=NEW.ticket_node_id AND project_id=NEW.project_id AND deleted_at IS NULL)
 OR NOT EXISTS (SELECT 1 FROM principals WHERE tenant_id=NEW.tenant_id AND id=NEW.requested_by_principal_id AND kind='person') THEN
  RAISE EXCEPTION 'invalid preparation project or requester' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER autopilot_preparation_requests_guard BEFORE INSERT ON autopilot_preparation_requests
 FOR EACH ROW EXECUTE FUNCTION aeon_autopilot_preparation_guard();
