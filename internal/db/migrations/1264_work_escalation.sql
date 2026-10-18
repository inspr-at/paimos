-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-729 tier A. New relation only; existing writers are unchanged.
SET LOCAL lock_timeout = '5s';
CREATE TABLE work_escalations (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 ticket_node_id uuid NOT NULL,
 project_id uuid NOT NULL,
 state jsonb NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,ticket_node_id),
 FOREIGN KEY(tenant_id,ticket_node_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 CHECK(jsonb_typeof(state)='object' AND octet_length(state::text)<=16384)
);
ALTER TABLE work_escalations ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_escalations FORCE ROW LEVEL SECURITY;
CREATE POLICY work_escalations_tenant ON work_escalations
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY work_escalations_visibility ON work_escalations AS RESTRICTIVE
 -- Follow the ticket's live ownership, including subtree moves and undo.
 -- The cached project_id is refreshed on save and is not an access boundary.
 USING((SELECT aeon_visible_all()) OR EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=work_escalations.tenant_id AND n.id=work_escalations.ticket_node_id))
 WITH CHECK((SELECT aeon_visible_all()) OR EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=work_escalations.tenant_id AND n.id=work_escalations.ticket_node_id));
