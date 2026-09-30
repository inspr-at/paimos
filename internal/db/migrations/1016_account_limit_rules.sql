-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-384: the Advanced sentence ("Let agents use at most 20% of this account
-- per day") and removable limits set by hand. Additive only: old manual windows
-- keep every field, and Remove marks a row instead of deleting it.
SET LOCAL lock_timeout = '5s';

CREATE TABLE account_limit_rules (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 account_id uuid NOT NULL,
 amount bigint NOT NULL CHECK (amount > 0),
 unit text NOT NULL CHECK (unit IN ('percent','runs','requests','tokens','cost_micros')),
 period text NOT NULL CHECK (period IN ('day','week','month')),
 -- Make this repeat: the old window the rule was made from.
 from_window_id uuid,
 created_by uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 removed_at timestamptz,
 PRIMARY KEY (tenant_id,id),
 FOREIGN KEY (tenant_id,account_id) REFERENCES agent_accounts(tenant_id,id),
 FOREIGN KEY (tenant_id,from_window_id) REFERENCES account_allowance_windows(tenant_id,id),
 FOREIGN KEY (tenant_id,created_by) REFERENCES principals(tenant_id,id),
 CHECK (unit<>'percent' OR amount<=100),
 CHECK (removed_at IS NULL OR removed_at>=created_at)
);
-- One sentence per account; a replaced rule stays as history.
CREATE UNIQUE INDEX account_limit_rules_current_idx ON account_limit_rules(tenant_id,account_id) WHERE removed_at IS NULL;
ALTER TABLE account_limit_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_limit_rules FORCE ROW LEVEL SECURITY;
CREATE POLICY account_limit_rules_tenant ON account_limit_rules
 USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

-- A removed manual window stops capping; its row, usage and reservations stay.
ALTER TABLE account_allowance_windows ADD COLUMN removed_at timestamptz;
