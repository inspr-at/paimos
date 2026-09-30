-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-383: a terminal vendor stop waits or creates one same-daemon retry.
ALTER TABLE agent_runs ADD COLUMN vendor_retry_pending boolean NOT NULL DEFAULT false;
ALTER TABLE agent_runs ADD COLUMN vendor_retry_at timestamptz;
ALTER TABLE agent_runs ADD COLUMN vendor_retry_same_account boolean NOT NULL DEFAULT false;
-- The handoff target is separate from the person's immutable account pin.
ALTER TABLE agent_runs ADD COLUMN retry_account_id uuid;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_retry_account_fk
 FOREIGN KEY (tenant_id,retry_account_id) REFERENCES agent_accounts(tenant_id,id);
CREATE INDEX agent_runs_vendor_retry_idx ON agent_runs(tenant_id,agent_principal_id,created_at)
 WHERE vendor_retry_pending;
