-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1004 expansion: the position of the flow projector in each tenant's
-- event log (PAIMOS rounds, review gates and holds). New table only.
SET LOCAL lock_timeout = '5s';
CREATE TABLE delivery_flow_cursors (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 last_event_id bigint NOT NULL CHECK(last_event_id >= 0),
 updated_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id)
);
ALTER TABLE delivery_flow_cursors ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_flow_cursors FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_flow_cursors_tenant ON delivery_flow_cursors
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
