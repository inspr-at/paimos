-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-263: one person approval publishes several rule sets (POST /rules/publish).
-- The complete answer is stored in the same transaction as the publication, keyed
-- by tenant, person and the digest of the canonical request, so an exact replay
-- returns that stored answer and nothing else: never a second version or event,
-- never another person's publication. Rows are append-only and visible only to
-- the rules API acting for that person (aeon.rules_owner is set per request).
CREATE TABLE rule_publish_batches (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    person_id uuid NOT NULL,
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    batch_id uuid NOT NULL,
    result jsonb NOT NULL CHECK (jsonb_typeof(result) = 'object'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, person_id, request_digest),
    UNIQUE (tenant_id, batch_id),
    FOREIGN KEY (tenant_id, person_id) REFERENCES principals(tenant_id, id)
);
ALTER TABLE rule_publish_batches ENABLE ROW LEVEL SECURITY;
ALTER TABLE rule_publish_batches FORCE ROW LEVEL SECURITY;
CREATE POLICY rule_publish_batches_tenant ON rule_publish_batches
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY rule_publish_batches_person ON rule_publish_batches AS RESTRICTIVE
    USING (coalesce(current_setting('aeon.rules_access', true), '') = 'on'
        AND person_id::text = coalesce(current_setting('aeon.rules_owner', true), ''))
    WITH CHECK (coalesce(current_setting('aeon.rules_access', true), '') = 'on'
        AND person_id::text = coalesce(current_setting('aeon.rules_owner', true), ''));
CREATE TRIGGER rule_publish_batches_immutable
    BEFORE UPDATE OR DELETE ON rule_publish_batches
    FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();
