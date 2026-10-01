-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

ALTER TABLE model_role_routes DROP CONSTRAINT model_role_routes_role_check;
ALTER TABLE model_role_routes ADD CONSTRAINT model_role_routes_role_check
    CHECK (role IN ('scout','mechanical','build','build-hard','review-gate','review-gate-security'));

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

DO $$
DECLARE tbl text;
BEGIN
    FOREACH tbl IN ARRAY ARRAY['model_refresh_settings','model_observations','model_report_receipts','model_discovery_credentials'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',tbl);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',tbl);
        EXECUTE format('CREATE POLICY %I ON %I USING (tenant_id=NULLIF(current_setting(''aeon.tenant_id'',true),'''')::uuid) WITH CHECK (tenant_id=NULLIF(current_setting(''aeon.tenant_id'',true),'''')::uuid)',tbl||'_tenant',tbl);
    END LOOP;
END $$;

-- New permissions are grantable; existing keys and roles are not expanded.
