-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-384: snapshots committed with the account/run reservation. The existing
-- tenant and project RLS on agent_runs also protects these estimates.
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_runs ADD COLUMN account_limit_estimates jsonb NOT NULL DEFAULT '{}'
 CHECK (jsonb_typeof(account_limit_estimates) = 'object');
