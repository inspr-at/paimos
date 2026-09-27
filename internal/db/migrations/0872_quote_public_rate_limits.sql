-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-133: sliding public-quote limits shared by every application replica.
-- The zero UUID is reserved for malformed or unknown public tenant selectors.
CREATE TABLE quote_public_rate_limits (
    tenant_id uuid NOT NULL,
    bucket_key text NOT NULL CHECK (bucket_key ~ '^[0-9a-f]{64}$'),
    attempts timestamptz[] NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, bucket_key),
    CHECK (cardinality(attempts) <= 120)
);
CREATE INDEX quote_public_rate_limits_cleanup ON quote_public_rate_limits (tenant_id, updated_at);
ALTER TABLE quote_public_rate_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE quote_public_rate_limits FORCE ROW LEVEL SECURITY;
CREATE POLICY quote_public_rate_limits_tenant ON quote_public_rate_limits
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
