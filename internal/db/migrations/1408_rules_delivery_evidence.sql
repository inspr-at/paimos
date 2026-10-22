-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1590: a served full-pack manifest is issuance, never delivery evidence.
-- Only the first matching post-start acknowledgement may fill the ack fields.
SET LOCAL lock_timeout = '5s';
CREATE TABLE harness_rules_deliveries (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 session_id uuid NOT NULL,
 run_id uuid NOT NULL,
 computer_id uuid NOT NULL,
 daemon_generation text NOT NULL,
 manifest jsonb NOT NULL,
 request_digest bytea NOT NULL,
 issued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT (clock_timestamp() + interval '5 minutes'),
 acknowledgement jsonb,
 acknowledged_at timestamptz,
 recorded_at timestamptz,
 PRIMARY KEY (tenant_id,id),
 UNIQUE (tenant_id,session_id)
);
ALTER TABLE harness_rules_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_rules_deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_rules_deliveries_tenant ON harness_rules_deliveries
 USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE FUNCTION aeon_rules_delivery_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'rules delivery evidence is immutable'; END IF;
 IF OLD.acknowledgement IS NOT NULL
 OR (to_jsonb(NEW) - ARRAY['acknowledgement','acknowledged_at','recorded_at']) IS DISTINCT FROM
    (to_jsonb(OLD) - ARRAY['acknowledgement','acknowledged_at','recorded_at'])
 OR NEW.acknowledgement IS NULL OR NEW.acknowledged_at IS NULL OR NEW.recorded_at IS NULL
 THEN RAISE EXCEPTION 'rules delivery evidence is immutable'; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER harness_rules_deliveries_immutable BEFORE UPDATE OR DELETE ON harness_rules_deliveries
 FOR EACH ROW EXECUTE FUNCTION aeon_rules_delivery_immutable();
