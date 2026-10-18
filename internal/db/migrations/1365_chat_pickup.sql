-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1487 expansion: older writers may omit all three nullable columns.
SET LOCAL lock_timeout = '5s';
ALTER TABLE inbox_delivery_settings ADD COLUMN chat_pickup_stall_seconds integer;
ALTER TABLE inbox_receipts ADD COLUMN pickup_stalled_at timestamptz;
ALTER TABLE inbox_messages ADD COLUMN pickup_lease_until timestamptz;
CREATE INDEX inbox_messages_chat_pickup ON inbox_messages(tenant_id,created_at,id)
 WHERE content_mode='durable' AND chat_thread_id IS NULL AND recipient_session_id IS NOT NULL
 AND fetched_at IS NULL AND acked_at IS NULL;
