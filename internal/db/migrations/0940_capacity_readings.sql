-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

CREATE TABLE account_capacity_readings (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 account_id uuid NOT NULL,
 window_kind text NOT NULL CHECK (window_kind IN ('5h','weekly','monthly','other')),
 bucket text NOT NULL DEFAULT '',
 window_minutes integer NOT NULL CHECK (window_minutes BETWEEN 1 AND 527040),
 used_percent numeric NOT NULL CHECK (used_percent BETWEEN 0 AND 100),
 resets_at timestamptz NOT NULL,
 read_at timestamptz NOT NULL,
 source text NOT NULL CHECK (source IN ('harness','agentd','estimate')),
 plan text NOT NULL DEFAULT '',
 ordinary_usage_allowed boolean,
 run_id uuid,
 phase text NOT NULL DEFAULT '' CHECK (phase IN ('','start','update','end')),
 PRIMARY KEY (tenant_id,id),
 FOREIGN KEY (tenant_id,account_id) REFERENCES agent_accounts(tenant_id,id),
 FOREIGN KEY (tenant_id,run_id) REFERENCES agent_runs(tenant_id,id),
 UNIQUE (tenant_id,account_id,window_kind,bucket,read_at,source,phase),
 CHECK (resets_at > read_at)
);
CREATE INDEX account_capacity_current_idx ON account_capacity_readings(tenant_id,account_id,window_kind,bucket,read_at DESC);
ALTER TABLE account_capacity_readings ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_capacity_readings FORCE ROW LEVEL SECURITY;
CREATE POLICY account_capacity_readings_tenant ON account_capacity_readings
 USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE TRIGGER account_capacity_readings_immutable BEFORE UPDATE OR DELETE ON account_capacity_readings
 FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();

ALTER TABLE account_allowance_windows DROP CONSTRAINT account_allowance_windows_unit_check;
ALTER TABLE account_allowance_windows ADD CONSTRAINT account_allowance_windows_unit_check CHECK (unit IN ('requests','tokens','cost_micros','percent'));
-- Vendor observations may exceed remaining headroom while reservations exist.
-- Keep those reservations intact; the router then fails closed until release.
ALTER TABLE account_allowance_windows DROP CONSTRAINT account_allowance_windows_check1;
ALTER TABLE account_allowance_windows ADD CONSTRAINT account_allowance_windows_budget_check CHECK (unit='percent' OR used+reserved<=allowance);
ALTER TABLE account_allowance_windows ADD COLUMN capacity_kind text;
ALTER TABLE account_allowance_windows ADD COLUMN capacity_bucket text NOT NULL DEFAULT '';
ALTER TABLE account_allowance_windows ADD COLUMN capacity_read_at timestamptz;
ALTER TABLE account_allowance_windows ADD COLUMN capacity_allowed boolean NOT NULL DEFAULT true;
CREATE UNIQUE INDEX account_capacity_window_idx ON account_allowance_windows(tenant_id,account_id,capacity_kind,capacity_bucket,ends_at) WHERE capacity_kind IS NOT NULL;

CREATE TABLE account_capacity_schedules (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 principal_id uuid NOT NULL,
 scope text NOT NULL CHECK (scope IN ('user','pool','account')),
 scope_key text NOT NULL,
 account_id uuid,
 schedule jsonb NOT NULL CHECK (jsonb_typeof(schedule)='object'),
 PRIMARY KEY (tenant_id,principal_id,scope,scope_key),
 FOREIGN KEY (tenant_id,principal_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,account_id) REFERENCES agent_accounts(tenant_id,id),
 CHECK ((scope='account')=(account_id IS NOT NULL)),
 CHECK (scope<>'account' OR scope_key=account_id::text)
);
ALTER TABLE account_capacity_schedules ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_capacity_schedules FORCE ROW LEVEL SECURITY;
CREATE POLICY account_capacity_schedules_tenant ON account_capacity_schedules
 USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
