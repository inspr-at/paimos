-- SPDX-License-Identifier: AGPL-3.0-only
-- Expand-only. Nullable epoch of the first permission pause for one
-- installation outage. Existing writers and tenant RLS are unchanged.
SET LOCAL lock_timeout = '5s';

ALTER TABLE delivery_webhook_receipts ADD COLUMN permission_paused_at timestamptz;

CREATE INDEX delivery_webhook_receipts_permission_pause ON delivery_webhook_receipts
 (tenant_id, installation_id, permission_paused_at, delivery_id)
 WHERE finished_at IS NULL AND permission_paused_at IS NOT NULL;
