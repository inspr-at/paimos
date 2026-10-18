-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1033: expand only. Rollback runs the older binary and retains this
-- additive schema and immutable observations; no existing writer is changed.
SET LOCAL lock_timeout = '5s';
ALTER TABLE personal_profiles ADD COLUMN daily_points_per_day integer;
ALTER TABLE agent_accounts ADD COLUMN daily_reset_policy text;
CREATE INDEX account_capacity_daily_first_idx ON account_capacity_readings
 (tenant_id,account_id,window_kind,bucket,resets_at,read_at) WHERE source<>'estimate';

-- Readiness facts are current-value rows. Retain vendor observations so daily
-- reads can use the first local-day sample instead of moving the baseline.
CREATE TABLE account_daily_observations (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 account_id uuid,
 person_id uuid,
 binding_revision bigint,
 resource_id uuid,
 window_key text,
 resets_at timestamptz,
 read_at timestamptz,
 used_pct double precision,
 PRIMARY KEY (tenant_id,id)
);
CREATE UNIQUE INDEX account_daily_observations_sample_idx ON account_daily_observations
 (tenant_id,resource_id,window_key,resets_at,read_at,account_id,binding_revision);
ALTER TABLE account_daily_observations ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_daily_observations FORCE ROW LEVEL SECURITY;
CREATE POLICY account_daily_observations_tenant ON account_daily_observations
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE TRIGGER account_daily_observations_immutable BEFORE UPDATE OR DELETE ON account_daily_observations
 FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();
CREATE FUNCTION aeon_capture_daily_observation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.used_percent IS NOT NULL AND NEW.reading_at IS NOT NULL AND NEW.resets_at IS NOT NULL THEN
  INSERT INTO account_daily_observations(tenant_id,account_id,person_id,binding_revision,resource_id,window_key,resets_at,read_at,used_pct)
  SELECT NEW.tenant_id,a.id,coalesce(p.linked_to,p.id),NEW.binding_revision,NEW.resource_id,NEW.window_key,NEW.resets_at,NEW.reading_at,NEW.used_percent
  FROM agent_accounts a LEFT JOIN principals p ON p.tenant_id=a.tenant_id AND p.id=a.owner_person_id AND p.kind='person'
  WHERE a.tenant_id=NEW.tenant_id AND a.id=NEW.reported_by_account_id AND a.link_revision=NEW.binding_revision
  AND NOT EXISTS (SELECT 1 FROM account_daily_observations o
   WHERE o.tenant_id=NEW.tenant_id AND o.resource_id=NEW.resource_id AND o.window_key=NEW.window_key
   AND o.resets_at=NEW.resets_at AND o.read_at=NEW.reading_at AND o.account_id=NEW.reported_by_account_id AND o.binding_revision=NEW.binding_revision);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER account_readiness_daily_observation AFTER INSERT OR UPDATE ON account_readiness_facts
 FOR EACH ROW EXECUTE FUNCTION aeon_capture_daily_observation();
