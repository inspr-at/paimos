-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-891 expansion: new shadow projections, no existing writers affected.
SET LOCAL lock_timeout = '5s';
CREATE TABLE delivery_ship_settings (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 snapshot jsonb NOT NULL,
 PRIMARY KEY(tenant_id,project_id),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id)
);
CREATE TABLE delivery_ship_decisions (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 request_id uuid NOT NULL,
 claimant_id uuid NOT NULL,
 source_round_id uuid NOT NULL,
 head_sha text NOT NULL,
 action_key text,
 position bigint NOT NULL,
 snapshot jsonb NOT NULL,
 PRIMARY KEY(tenant_id,project_id,request_id),
 UNIQUE(tenant_id,project_id,action_key),
 UNIQUE(tenant_id,project_id,position),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY(tenant_id,claimant_id) REFERENCES principals(tenant_id,id)
);
ALTER TABLE delivery_ship_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_ship_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_ship_settings_tenant ON delivery_ship_settings
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_ship_settings_project ON delivery_ship_settings AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));
ALTER TABLE delivery_ship_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_ship_decisions FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_ship_decisions_tenant ON delivery_ship_decisions
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_ship_decisions_project ON delivery_ship_decisions AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));
