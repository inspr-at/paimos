-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-215: immutable tenant list prices and cumulative session/model usage.
-- No default vendor prices: absent pricing is unknown, never zero.
CREATE TABLE model_prices (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    model text NOT NULL CHECK (length(model) BETWEEN 1 AND 128 AND model ~ '^[A-Za-z0-9][A-Za-z0-9._:/-]*$'),
    version bigint NOT NULL CHECK (version BETWEEN 1 AND 1000000000000),
    input_usd_per_million numeric(13,6) NOT NULL CHECK (input_usd_per_million BETWEEN 0 AND 1000000),
    output_usd_per_million numeric(13,6) NOT NULL CHECK (output_usd_per_million BETWEEN 0 AND 1000000),
    cached_input_usd_per_million numeric(13,6) NOT NULL CHECK (cached_input_usd_per_million BETWEEN 0 AND 1000000),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, model, version)
);
ALTER TABLE model_prices ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_prices FORCE ROW LEVEL SECURITY;
CREATE POLICY model_prices_tenant ON model_prices
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE TRIGGER model_prices_immutable BEFORE UPDATE OR DELETE ON model_prices
    FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();

-- Consumers sum these current rows, never receipts or overlapping run telemetry.
CREATE TABLE harness_session_usage (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    session_id uuid NOT NULL,
    model text NOT NULL CHECK (length(model) BETWEEN 1 AND 128 AND model ~ '^[A-Za-z0-9][A-Za-z0-9._:/-]*$'),
    sequence bigint NOT NULL CHECK (sequence BETWEEN 1 AND 1000000000000),
    input_tokens bigint CHECK (input_tokens BETWEEN 0 AND 1000000000000),
    output_tokens bigint CHECK (output_tokens BETWEEN 0 AND 1000000000000),
    cached_input_tokens bigint CHECK (cached_input_tokens BETWEEN 0 AND 1000000000000),
    provisional boolean NOT NULL,
    price_version bigint,
    estimated_cost_usd numeric(30,12) CHECK (estimated_cost_usd >= 0),
    account_id uuid,
    account_label text CHECK (char_length(account_label) BETWEEN 1 AND 128),
    billing_mode text NOT NULL CHECK (billing_mode IN ('unknown','api','subscription')),
    subscription_label text CHECK (char_length(subscription_label) BETWEEN 1 AND 120),
    reported_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, session_id, model),
    FOREIGN KEY (tenant_id, session_id) REFERENCES harness_sessions(tenant_id, id),
    FOREIGN KEY (tenant_id, model, price_version) REFERENCES model_prices(tenant_id, model, version),
    FOREIGN KEY (tenant_id, account_id) REFERENCES agent_accounts(tenant_id, id),
    CHECK (cached_input_tokens <= input_tokens),
    CHECK (provisional OR (input_tokens IS NOT NULL AND output_tokens IS NOT NULL AND cached_input_tokens IS NOT NULL)),
    CHECK ((estimated_cost_usd IS NOT NULL) = (price_version IS NOT NULL AND input_tokens IS NOT NULL AND output_tokens IS NOT NULL AND cached_input_tokens IS NOT NULL)),
    CHECK (subscription_label IS NULL OR billing_mode = 'subscription')
);
ALTER TABLE harness_session_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_session_usage FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_session_usage_tenant ON harness_session_usage
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY harness_session_usage_visibility ON harness_session_usage AS RESTRICTIVE
    USING (EXISTS (SELECT 1 FROM harness_sessions s WHERE s.tenant_id = harness_session_usage.tenant_id AND s.id = harness_session_usage.session_id))
    WITH CHECK (EXISTS (SELECT 1 FROM harness_sessions s WHERE s.tenant_id = harness_session_usage.tenant_id AND s.id = harness_session_usage.session_id));

CREATE TABLE harness_usage_receipts (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    session_id uuid NOT NULL,
    report_id uuid NOT NULL,
    model text NOT NULL,
    sequence bigint NOT NULL CHECK (sequence BETWEEN 1 AND 1000000000000),
    payload_digest bytea NOT NULL CHECK (octet_length(payload_digest) = 32),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, session_id, report_id),
    UNIQUE (tenant_id, session_id, model, sequence),
    FOREIGN KEY (tenant_id, session_id, model) REFERENCES harness_session_usage(tenant_id, session_id, model)
);
ALTER TABLE harness_usage_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_usage_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_usage_receipts_tenant ON harness_usage_receipts
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY harness_usage_receipts_visibility ON harness_usage_receipts AS RESTRICTIVE
    USING (EXISTS (SELECT 1 FROM harness_sessions s WHERE s.tenant_id = harness_usage_receipts.tenant_id AND s.id = harness_usage_receipts.session_id))
    WITH CHECK (EXISTS (SELECT 1 FROM harness_sessions s WHERE s.tenant_id = harness_usage_receipts.tenant_id AND s.id = harness_usage_receipts.session_id));
CREATE TRIGGER harness_usage_receipts_immutable BEFORE UPDATE OR DELETE ON harness_usage_receipts
    FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();
