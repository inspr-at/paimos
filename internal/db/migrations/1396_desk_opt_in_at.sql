-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1474: when this device turned Desk notices on. Null while they are off.
-- Older writers leave the column null; a later opt-in records the time.
ALTER TABLE phone_push_subscriptions ADD COLUMN desk_enabled_at timestamptz;
