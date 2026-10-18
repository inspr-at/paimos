-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-888 expansion: new shadow projections; older binaries have no writers.
SET LOCAL lock_timeout = '5s';
CREATE TABLE delivery_work_rounds (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 id uuid NOT NULL,
 ticket_node_id uuid NOT NULL,
 slug text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('first_build','fix','merge','land')),
 round_number integer NOT NULL CHECK(round_number BETWEEN 1 AND 100),
 state text NOT NULL CHECK(state IN ('queued','claimed','running','done','parked')),
 position bigint NOT NULL,
 snapshot jsonb NOT NULL,
 PRIMARY KEY(tenant_id,id),
 UNIQUE(tenant_id,project_id,ticket_node_id,kind,round_number),
 UNIQUE(tenant_id,project_id,position),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY(tenant_id,ticket_node_id) REFERENCES nodes(tenant_id,id)
);
CREATE UNIQUE INDEX delivery_work_active_slug ON delivery_work_rounds(tenant_id,project_id,slug) WHERE state IN ('claimed','running');
CREATE INDEX delivery_work_order ON delivery_work_rounds(tenant_id,project_id,state,position);
CREATE INDEX delivery_work_ticket ON delivery_work_rounds(tenant_id,project_id,ticket_node_id,position);
ALTER TABLE delivery_work_rounds ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_work_rounds FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_work_rounds_tenant ON delivery_work_rounds
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_work_rounds_project ON delivery_work_rounds AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));

CREATE TABLE delivery_work_settings (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 snapshot jsonb NOT NULL,
 PRIMARY KEY(tenant_id,project_id),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id)
);
ALTER TABLE delivery_work_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_work_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_work_settings_tenant ON delivery_work_settings
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_work_settings_project ON delivery_work_settings AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));

CREATE TABLE delivery_work_claims (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 request_id uuid NOT NULL,
 claimant_id uuid NOT NULL,
 snapshot jsonb NOT NULL,
 PRIMARY KEY(tenant_id,project_id,request_id),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY(tenant_id,claimant_id) REFERENCES principals(tenant_id,id)
);
ALTER TABLE delivery_work_claims ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_work_claims FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_work_claims_tenant ON delivery_work_claims
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_work_claims_project ON delivery_work_claims AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));
