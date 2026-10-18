-- SPDX-License-Identifier: AGPL-3.0-only
-- T5 consumes its one-shot cause on EVERY insert, including disabled pins.
-- Audit is deferred so legacy writers can finish their resource locks first.
CREATE FUNCTION aeon_model_activation_fence() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    cause text := coalesce(nullif(current_setting('aeon.model_activation_cause',true),''),'none');
    rule text;
    rule_revision bigint;
    activated timestamptz;
BEGIN
    PERFORM set_config('aeon.model_activation_cause','',true);
    IF NEW.enabled IS DISTINCT FROM true THEN RETURN NEW; END IF;
    SELECT new_models,revision,enforced_at INTO STRICT rule,rule_revision,activated
        FROM account_use_rules WHERE tenant_id=NEW.tenant_id;
    IF activated IS NULL OR rule='allow' OR cause='person'
        OR (rule='shipped_only' AND cause='shipped_catalog') THEN
        RETURN NEW;
    END IF;
    NEW.enabled := false;
    INSERT INTO model_observations(tenant_id,harness,model,effort,source)
        VALUES(NEW.tenant_id,NEW.harness,NEW.model,NEW.effort,'activation_withheld')
        ON CONFLICT(tenant_id,harness,model,effort) DO UPDATE SET last_seen_at=now();
    PERFORM set_config('aeon.model_withheld_'||replace(NEW.tenant_id::text,'-','')||'_'||replace(NEW.id::text,'-',''),
        jsonb_build_object('profile_id',NEW.id,'rule',rule,'rule_revision',rule_revision,'cause',cause)::text,true);
    RETURN NEW;
END $$;
CREATE TRIGGER model_profiles_activation_fence BEFORE INSERT ON model_profiles
    FOR EACH ROW EXECUTE FUNCTION aeon_model_activation_fence();

CREATE FUNCTION aeon_model_activation_audit() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    payload text := current_setting('aeon.model_withheld_'||replace(NEW.tenant_id::text,'-','')||'_'||replace(NEW.id::text,'-',''),true);
    actor uuid;
BEGIN
    IF nullif(payload,'') IS NOT NULL THEN
        SELECT id INTO STRICT actor FROM principals WHERE tenant_id=NEW.tenant_id
            AND kind='agent' AND name='System' AND roles @> ARRAY['system']::text[];
        INSERT INTO events(tenant_id,actor_principal_id,type,after)
            VALUES(NEW.tenant_id,actor,'model.activation_withheld',payload::jsonb);
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER model_profiles_activation_audit AFTER INSERT ON model_profiles
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION aeon_model_activation_audit();
