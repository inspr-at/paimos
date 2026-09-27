-- SPDX-License-Identifier: AGPL-3.0-only
-- The subject is private retry state. It is never returned in members or events.
ALTER TABLE invites ADD COLUMN account_status text
    CHECK (account_status IN ('processing', 'invited', 'exists', 'failed'));
ALTER TABLE invites ADD COLUMN account_subject text;
ALTER TABLE invites ADD COLUMN account_display_name text;
ALTER TABLE invites ADD COLUMN account_started_at timestamptz;
