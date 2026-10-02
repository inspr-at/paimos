-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
ALTER TABLE harness_sessions
  ADD COLUMN service_tier text CHECK (service_tier IN ('default','fast','fastest')),
  ADD COLUMN service_tier_revision bigint NOT NULL DEFAULT 0 CHECK (service_tier_revision >= 0),
  ADD COLUMN service_tier_reports jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(service_tier_reports)='array');

ALTER TABLE harness_controls DROP CONSTRAINT harness_controls_kind_check;
ALTER TABLE harness_controls ADD CONSTRAINT harness_controls_kind_check CHECK (kind IN
  ('interrupt','stop','force_stop','steer','rename','model','effort','rename_request','model_request','tier'));
ALTER TABLE harness_controls DROP CONSTRAINT harness_settings_value;
ALTER TABLE harness_controls ADD CONSTRAINT harness_settings_value CHECK (
 CASE WHEN kind IN ('rename','model','effort','tier') THEN
  value IS NOT NULL AND char_length(value) BETWEEN 1 AND 128
  AND value=btrim(value) AND value !~ '[[:cntrl:]]'
  AND expected_ownership IS NOT NULL AND request_digest IS NOT NULL AND expires_at IS NOT NULL
 ELSE value IS NULL END);
ALTER TABLE harness_controls ADD CONSTRAINT harness_tier_value CHECK (kind<>'tier' OR value IN ('default','fast','fastest'));

CREATE TABLE harness_tier_requests (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL,
 session_id uuid NOT NULL,
 tier text NOT NULL CHECK (tier IN ('default','fast','fastest')),
 reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 500),
 requested_by_principal_id uuid NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','approved','declined')),
 decided_by_principal_id uuid,
 control_id uuid,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 decided_at timestamptz,
 PRIMARY KEY (tenant_id,id),
 FOREIGN KEY (tenant_id,session_id) REFERENCES harness_sessions(tenant_id,id),
 FOREIGN KEY (tenant_id,requested_by_principal_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,decided_by_principal_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,control_id) REFERENCES harness_controls(tenant_id,id),
 CHECK ((state='pending')=(decided_at IS NULL)),
 CHECK ((state='pending')=(decided_by_principal_id IS NULL)),
 CHECK (state='approved' OR control_id IS NULL)
);
CREATE UNIQUE INDEX harness_tier_request_pending ON harness_tier_requests(tenant_id,session_id) WHERE state='pending';
ALTER TABLE harness_tier_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_tier_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_tier_requests_tenant ON harness_tier_requests
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

-- Each cumulative report retains its tier-specific token segments. A later
-- switch never reprices previously observed tokens at the new multiplier.
ALTER TABLE harness_session_usage
 ADD COLUMN service_tier text CHECK (service_tier IN ('default','fast','fastest')),
 ADD COLUMN tier_segments jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(tier_segments)='array');

ALTER TABLE run_telemetry ADD COLUMN service_tier text CHECK (service_tier IN ('default','fast','fastest'));
