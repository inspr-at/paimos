-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-533: protect tenants present at upgrade, before any startup worker runs.
SET LOCAL lock_timeout = '5s';
CREATE TABLE status_autopilot_upgrade (
 tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
 suggest_until timestamptz NOT NULL,
 confirmed_at timestamptz
);
-- Seed the new table before enabling RLS. New tenants have no grace row.
INSERT INTO status_autopilot_upgrade(tenant_id,suggest_until)
 SELECT id,clock_timestamp()+interval '24 hours' FROM tenants;
ALTER TABLE status_autopilot_upgrade ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_autopilot_upgrade FORCE ROW LEVEL SECURITY;
CREATE POLICY status_autopilot_upgrade_tenant ON status_autopilot_upgrade
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

ALTER TABLE status_autopilot_days ADD COLUMN mode text NOT NULL DEFAULT 'on';

CREATE TABLE status_autopilot_proposals (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 event_id bigint NOT NULL,
 node_id uuid NOT NULL,
 rule text NOT NULL,
 anchor text NOT NULL,
 decision jsonb NOT NULL CHECK (jsonb_typeof(decision)='object'),
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','applied','dismissed')),
 PRIMARY KEY (tenant_id,event_id),
 UNIQUE (tenant_id,node_id,rule,anchor),
 FOREIGN KEY (tenant_id,event_id) REFERENCES events(tenant_id,id),
 FOREIGN KEY (tenant_id,node_id) REFERENCES nodes(tenant_id,id)
);
CREATE INDEX status_autopilot_proposals_pending ON status_autopilot_proposals(tenant_id,event_id)
 WHERE status='pending';
ALTER TABLE status_autopilot_proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_autopilot_proposals FORCE ROW LEVEL SECURITY;
CREATE POLICY status_autopilot_proposals_tenant ON status_autopilot_proposals
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY status_autopilot_proposals_visibility ON status_autopilot_proposals AS RESTRICTIVE
 USING (EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=status_autopilot_proposals.tenant_id AND n.id=status_autopilot_proposals.node_id))
 WITH CHECK (EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=status_autopilot_proposals.tenant_id AND n.id=status_autopilot_proposals.node_id));
