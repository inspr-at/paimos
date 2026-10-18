-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-885: expand-only index for bounded topic/cursor replay.
-- No tables or columns are added; existing DSAR classifications stay complete.
SET LOCAL lock_timeout = '5s';
CREATE INDEX events_subscription_type_cursor_idx ON events (tenant_id,type,id);
