-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1360 D5: attribution only. The legacy same-project parent FK is unchanged.
CREATE TABLE harness_session_lineage (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    child_session_id uuid NOT NULL,
    parent_session_id uuid NOT NULL,
    owner_person_id uuid NOT NULL,
    PRIMARY KEY (tenant_id,child_session_id),
    FOREIGN KEY (tenant_id,child_session_id) REFERENCES harness_sessions(tenant_id,id),
    FOREIGN KEY (tenant_id,parent_session_id) REFERENCES harness_sessions(tenant_id,id),
    FOREIGN KEY (tenant_id,owner_person_id) REFERENCES principals(tenant_id,id),
    CHECK (child_session_id <> parent_session_id)
);
CREATE INDEX harness_session_lineage_parent ON harness_session_lineage(tenant_id,parent_session_id);
ALTER TABLE harness_session_lineage ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_session_lineage FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_session_lineage_tenant ON harness_session_lineage
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
