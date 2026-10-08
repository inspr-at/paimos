-- SPDX-License-Identifier: AGPL-3.0-only
-- Expansion: older binaries have no writers for this new audit table.
SET LOCAL lock_timeout = '5s';
-- A distinct nullable fact field keeps the older projection's observations
-- replay format intact. Older writers simply leave it null.
ALTER TABLE delivery_github_events ADD COLUMN audit_checks jsonb;
CREATE TABLE delivery_merge_audit (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    repository text NOT NULL,
    merge_sha text NOT NULL,
    pull_request bigint,
    ticket_node_id uuid,
    project_id uuid,
    merged_by text NOT NULL,
    via_queue boolean NOT NULL,
    checks_passed boolean NOT NULL,
    review_ok boolean NOT NULL,
    flags text[] NOT NULL,
    at timestamptz NOT NULL,
    alerted_at timestamptz,
    PRIMARY KEY (tenant_id, repository, merge_sha),
    FOREIGN KEY (tenant_id, ticket_node_id) REFERENCES nodes(tenant_id,id),
    FOREIGN KEY (tenant_id, project_id) REFERENCES nodes(tenant_id,id)
);
CREATE INDEX delivery_audit_project ON delivery_merge_audit(tenant_id,project_id,repository,merge_sha);
CREATE INDEX delivery_audit_pending ON delivery_merge_audit(tenant_id,repository,merge_sha)
    WHERE cardinality(flags)>0 AND alerted_at IS NULL;
ALTER TABLE delivery_merge_audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_merge_audit FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_audit_tenant ON delivery_merge_audit
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_audit_project ON delivery_merge_audit AS RESTRICTIVE
    USING (((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
        AND (ticket_node_id IS NULL OR EXISTS (SELECT 1 FROM nodes n WHERE n.tenant_id=delivery_merge_audit.tenant_id AND n.id=ticket_node_id AND n.deleted_at IS NULL)))
    WITH CHECK (((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
        AND (ticket_node_id IS NULL OR EXISTS (SELECT 1 FROM nodes n WHERE n.tenant_id=delivery_merge_audit.tenant_id AND n.id=ticket_node_id AND n.deleted_at IS NULL)));
