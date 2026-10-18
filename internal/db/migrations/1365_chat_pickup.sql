-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1487 expansion: older writers may omit all three nullable columns.
SET LOCAL lock_timeout = '5s';
ALTER TABLE inbox_delivery_settings ADD COLUMN chat_pickup_stall_seconds integer;
ALTER TABLE inbox_receipts ADD COLUMN pickup_stalled_at timestamptz;
ALTER TABLE inbox_messages ADD COLUMN pickup_lease_until timestamptz;
