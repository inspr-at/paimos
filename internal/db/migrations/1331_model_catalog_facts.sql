-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

-- Public facts are independent of immutable dispatch pins and account grants.
-- The route is empty for provider-wide facts; harness facts retain the exact
-- launch identifier. Multiple sources remain available for field-level precedence.
CREATE TABLE model_catalog_facts (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    provider text NOT NULL,
    model_id text NOT NULL,
    route text NOT NULL DEFAULT '',
    fact text NOT NULL,
    value jsonb NOT NULL,
    source text NOT NULL,
    source_kind text NOT NULL,
    field_path text NOT NULL,
    fetched_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id,provider,model_id,route,fact,source)
);
ALTER TABLE model_catalog_facts ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_catalog_facts FORCE ROW LEVEL SECURITY;
CREATE POLICY model_catalog_facts_tenant ON model_catalog_facts
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

-- Add Google without changing the released vendor CHECK or vault format.
CREATE TABLE model_catalog_credentials (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    account_id uuid NOT NULL,
    vendor text NOT NULL CHECK (vendor='google'),
    ciphertext bytea NOT NULL,
    PRIMARY KEY (tenant_id,account_id),
    FOREIGN KEY (tenant_id,account_id) REFERENCES agent_accounts(tenant_id,id)
);
ALTER TABLE model_catalog_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_catalog_credentials FORCE ROW LEVEL SECURITY;
CREATE POLICY model_catalog_credentials_tenant ON model_catalog_credentials
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

-- The old scale/display functions remain readable for released pins. Their
-- guessed output must never become metadata on a newly inserted pin, including
-- inserts by an older server. Explicit display overrides remain supported.
CREATE FUNCTION aeon_model_catalog_display_insert() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE p model_profiles%ROWTYPE;
BEGIN
    SELECT * INTO p FROM model_profiles
        WHERE tenant_id=NEW.tenant_id AND id=NEW.profile_id;
    NEW.effort_level := NULL;
    NEW.model_display := jsonb_build_object(
        'display_name',coalesce(p.display_overrides->>'display_name',p.model),
        'short_name',coalesce(p.display_overrides->>'short_name',p.model),
        'model_version',coalesce(p.display_overrides->>'model_version',''));
    RETURN NEW;
END $$;
CREATE TRIGGER model_catalog_display_insert BEFORE INSERT ON model_profile_display
    FOR EACH ROW EXECUTE FUNCTION aeon_model_catalog_display_insert();
