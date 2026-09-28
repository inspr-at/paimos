-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-249: distinct received reports, never instruction provenance or authority.
-- A table is necessary: events' restrictive visibility may hide harness history
-- from project readers or hide rule-node references. Deriving CAS from that
-- partial history would be unsafe. Reuse provenance's session RLS and the existing
-- append-only trigger, and emit a content-free audit event in the same transaction.
-- The typed API bounds/allowlists metadata before storage; no instruction bodies,
-- paths, credentials, arbitrary version labels or execution claims are accepted.
CREATE TABLE harness_rules_receipts (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    session_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    request_id uuid NOT NULL,
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    request jsonb NOT NULL CHECK (jsonb_typeof(request) = 'object' AND octet_length(request::text) <= 2048),
    recorded_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, session_id, revision),
    UNIQUE (tenant_id, session_id, request_id),
    FOREIGN KEY (tenant_id, session_id) REFERENCES harness_sessions(tenant_id, id),
    FOREIGN KEY (tenant_id, recorded_by) REFERENCES principals(tenant_id, id)
);
ALTER TABLE harness_rules_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_rules_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_rules_receipts_tenant ON harness_rules_receipts
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY harness_rules_receipts_project_visibility ON harness_rules_receipts AS RESTRICTIVE
    USING (EXISTS (SELECT 1 FROM harness_sessions s
        WHERE s.tenant_id = harness_rules_receipts.tenant_id AND s.id = harness_rules_receipts.session_id))
    WITH CHECK (EXISTS (SELECT 1 FROM harness_sessions s
        WHERE s.tenant_id = harness_rules_receipts.tenant_id AND s.id = harness_rules_receipts.session_id));
CREATE TRIGGER harness_rules_receipts_immutable
    BEFORE UPDATE OR DELETE ON harness_rules_receipts
    FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();
