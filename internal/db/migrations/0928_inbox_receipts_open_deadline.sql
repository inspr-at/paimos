-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
CREATE INDEX CONCURRENTLY inbox_receipts_open_deadline ON inbox_receipts(tenant_id, deliver_by) WHERE state = 'queued' AND deliver_by IS NOT NULL;
