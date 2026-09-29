-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
ALTER TABLE account_allowance_windows ADD COLUMN capacity_source text NOT NULL DEFAULT '';
ALTER TABLE account_allowance_windows ADD COLUMN capacity_retired boolean NOT NULL DEFAULT false;
ALTER TABLE account_allowance_windows ADD COLUMN capacity_refresh_run uuid;
ALTER TABLE account_allowance_windows ADD FOREIGN KEY (tenant_id,capacity_refresh_run) REFERENCES agent_runs(tenant_id,id);
UPDATE account_allowance_windows w SET capacity_source = COALESCE((SELECT source FROM account_capacity_readings r WHERE r.tenant_id=w.tenant_id AND r.account_id=w.account_id AND r.window_kind=w.capacity_kind AND r.bucket=w.capacity_bucket AND r.read_at=w.capacity_read_at ORDER BY CASE source WHEN 'harness' THEN 0 ELSE 1 END LIMIT 1),'') WHERE capacity_kind IS NOT NULL;
ALTER TABLE agent_accounts ADD COLUMN capacity_owner uuid;
ALTER TABLE agent_accounts ADD FOREIGN KEY (tenant_id,capacity_owner) REFERENCES principals(tenant_id,id);
-- Preserve an existing unambiguous account schedule owner during upgrade.
UPDATE agent_accounts a SET capacity_owner=s.principal_id FROM (
 SELECT tenant_id,account_id,(array_agg(principal_id))[1] AS principal_id
 FROM account_capacity_schedules WHERE scope='account'
 GROUP BY tenant_id,account_id HAVING count(*)=1
) s WHERE s.tenant_id=a.tenant_id AND s.account_id=a.id;
