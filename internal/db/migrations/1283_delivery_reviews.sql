-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-890 expansion: shadow-only projections, with no older binary writers.
SET LOCAL lock_timeout = '5s';
CREATE TABLE delivery_review_rounds (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 id uuid NOT NULL,
 source_round_id uuid NOT NULL,
 ticket_node_id uuid NOT NULL,
 slug text NOT NULL,
 repository text NOT NULL,
 head_sha text NOT NULL,
 author_run_id uuid NOT NULL,
 state text NOT NULL CHECK(state IN ('queued','claimed','completed')),
 position bigint NOT NULL,
 snapshot jsonb NOT NULL,
 PRIMARY KEY(tenant_id,id),
 UNIQUE(tenant_id,project_id,source_round_id),
 UNIQUE(tenant_id,project_id,slug,repository,head_sha),
 UNIQUE(tenant_id,project_id,author_run_id),
 UNIQUE(tenant_id,project_id,position),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY(tenant_id,ticket_node_id) REFERENCES nodes(tenant_id,id),
 -- The source queue is an independently rebuildable projection. Do not pin
 -- its physical row with a foreign key; current bindings are checked in tx.
 FOREIGN KEY(tenant_id,author_run_id) REFERENCES agent_runs(tenant_id,id)
);
CREATE INDEX delivery_review_order ON delivery_review_rounds(tenant_id,project_id,slug,position DESC);

CREATE TABLE delivery_review_settings (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 snapshot jsonb NOT NULL,
 PRIMARY KEY(tenant_id,project_id),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id)
);
CREATE TABLE delivery_review_claims (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 request_id uuid NOT NULL,
 snapshot jsonb NOT NULL,
 PRIMARY KEY(tenant_id,project_id,request_id),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id)
);
ALTER TABLE delivery_review_rounds ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_review_rounds FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_review_rounds_tenant ON delivery_review_rounds
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_review_rounds_project ON delivery_review_rounds AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));
ALTER TABLE delivery_review_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_review_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_review_settings_tenant ON delivery_review_settings
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_review_settings_project ON delivery_review_settings AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));
ALTER TABLE delivery_review_claims ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_review_claims FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_review_claims_tenant ON delivery_review_claims
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_review_claims_project ON delivery_review_claims AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));
