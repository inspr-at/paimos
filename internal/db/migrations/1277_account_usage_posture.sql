-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
-- Nullable expansion: old account writers remain valid. Overrides bind to the
-- owning person and link revision so an ownership change never inherits them.
ALTER TABLE agent_accounts
 ADD COLUMN usage_posture text,
 ADD COLUMN usage_posture_person_id uuid,
 ADD COLUMN usage_posture_link_revision bigint,
 ADD COLUMN usage_floor_percent integer,
 ADD COLUMN usage_revision bigint;
