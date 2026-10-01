-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

-- Keep the published role table and its CHECK unchanged for previous binaries.
-- The new role lives in an additive table with the same pin/override invariants.
CREATE TABLE model_security_role_routes (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    role text NOT NULL CHECK (role = 'review-gate-security'),
    priority integer NOT NULL CHECK (priority > 0),
    profile_id uuid NOT NULL,
    valid_until timestamptz,
    state text NOT NULL DEFAULT 'available'
        CHECK (state IN ('available', 'unavailable', 'conserved', 'budget_limited')),
    reason text NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, role, priority),
    UNIQUE (tenant_id, role, profile_id),
    FOREIGN KEY (tenant_id, profile_id) REFERENCES model_profiles(tenant_id, id),
    CHECK (state = 'available' OR (valid_until IS NOT NULL AND length(btrim(reason)) > 0))
);
ALTER TABLE model_security_role_routes ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_security_role_routes FORCE ROW LEVEL SECURITY;
CREATE POLICY model_security_role_routes_tenant ON model_security_role_routes
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

CREATE TABLE model_refresh_settings (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
    catalog_version text NOT NULL DEFAULT '',
    agent_reports_enabled boolean NOT NULL DEFAULT true,
    auto_add_profiles boolean NOT NULL DEFAULT true,
    api_enabled boolean NOT NULL DEFAULT false,
    interval_minutes integer NOT NULL DEFAULT 1440 CHECK (interval_minutes BETWEEN 60 AND 43200),
    last_run_at timestamptz,
    last_result jsonb NOT NULL DEFAULT '{}'
);

-- Separate from immutable profile pins: health never destroys run history.
CREATE TABLE model_observations (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    harness text NOT NULL CHECK (harness IN ('codex','claude','pi','cursor','grok')),
    model text NOT NULL CHECK (length(model) BETWEEN 1 AND 128),
    effort text NOT NULL CHECK (length(effort) BETWEEN 1 AND 32),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    last_working_at timestamptz,
    last_failing_at timestamptz,
    failures integer NOT NULL DEFAULT 0 CHECK (failures >= 0),
    suppressed_until timestamptz,
    source text NOT NULL,
    PRIMARY KEY (tenant_id,harness,model,effort)
);

CREATE TABLE model_report_receipts (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    principal_id uuid NOT NULL,
    report_id uuid NOT NULL,
    content jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id,principal_id,report_id),
    FOREIGN KEY (tenant_id,principal_id) REFERENCES principals(tenant_id,id)
);

-- Narrow model-list vault. Never exposed by any read endpoint or event.
CREATE TABLE model_discovery_credentials (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    account_id uuid NOT NULL,
    vendor text NOT NULL CHECK (vendor IN ('openai','anthropic','xai','openrouter')),
    ciphertext bytea NOT NULL,
    PRIMARY KEY (tenant_id,account_id),
    FOREIGN KEY (tenant_id,account_id) REFERENCES agent_accounts(tenant_id,id)
);

-- Explicit RLS declarations keep this expansion inspectable by the guard.
ALTER TABLE model_refresh_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_refresh_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY model_refresh_settings_tenant ON model_refresh_settings
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

ALTER TABLE model_observations ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_observations FORCE ROW LEVEL SECURITY;
CREATE POLICY model_observations_tenant ON model_observations
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

ALTER TABLE model_report_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_report_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY model_report_receipts_tenant ON model_report_receipts
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

ALTER TABLE model_discovery_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_discovery_credentials FORCE ROW LEVEL SECURITY;
CREATE POLICY model_discovery_credentials_tenant ON model_discovery_credentials
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

-- New permissions are grantable; existing keys and roles are not expanded.
