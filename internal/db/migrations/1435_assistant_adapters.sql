-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1702: extend the inbox target, not the principal or delivery model.
CREATE TABLE assistant_adapters (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    target_id uuid NOT NULL,
    vendor text NOT NULL,
    last_success_at timestamptz,
    last_failure_at timestamptz,
    last_failure_code text,
    PRIMARY KEY (tenant_id, target_id),
    FOREIGN KEY (tenant_id, target_id) REFERENCES inbox_message_targets(tenant_id, id)
);
-- Rolling upgrades keep accepting the legacy target configuration, including
-- its immutable ciphertext/AAD and IDs referenced by pending deliveries.
CREATE FUNCTION aeon_register_assistant_adapter() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.adapter='grok_bot_routine' THEN
        INSERT INTO assistant_adapters(tenant_id,target_id,vendor)
        VALUES(NEW.tenant_id,NEW.id,'grok');
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER inbox_assistant_adapter_registered AFTER INSERT ON inbox_message_targets
    FOR EACH ROW EXECUTE FUNCTION aeon_register_assistant_adapter();

-- Return legacy bindings under each tenant's existing RLS. Insert into the
-- new table before enabling its RLS; no old rows or ciphertext are changed.
CREATE FUNCTION aeon_legacy_assistant_adapters()
RETURNS TABLE (tenant_id uuid, target_id uuid, vendor text) LANGUAGE plpgsql AS $$
DECLARE
    tenant uuid;
    prior_tenant text := current_setting('aeon.tenant_id',true);
    prior_visible text := current_setting('aeon.visible_projects',true);
BEGIN
    PERFORM set_config('aeon.visible_projects','*',true);
    FOR tenant IN SELECT id FROM tenants LOOP
        PERFORM set_config('aeon.tenant_id',tenant::text,true);
        RETURN QUERY SELECT t.tenant_id,t.id,'grok'::text FROM inbox_message_targets t
            WHERE t.tenant_id=tenant AND t.adapter='grok_bot_routine';
    END LOOP;
    PERFORM set_config('aeon.tenant_id',coalesce(prior_tenant,''),true);
    PERFORM set_config('aeon.visible_projects',coalesce(prior_visible,''),true);
END $$;
INSERT INTO assistant_adapters(tenant_id,target_id,vendor)
    SELECT tenant_id,target_id,vendor FROM aeon_legacy_assistant_adapters();

ALTER TABLE assistant_adapters ENABLE ROW LEVEL SECURITY;
ALTER TABLE assistant_adapters FORCE ROW LEVEL SECURITY;
CREATE POLICY assistant_adapters_tenant ON assistant_adapters
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY assistant_adapters_visible ON assistant_adapters AS RESTRICTIVE FOR SELECT
    USING (EXISTS (SELECT 1 FROM inbox_message_targets t JOIN nodes n ON n.tenant_id=t.tenant_id AND n.id=t.project_id
        WHERE t.tenant_id=assistant_adapters.tenant_id AND t.id=assistant_adapters.target_id));

