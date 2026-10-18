-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
-- Nullable expansion of the usage posture model; an account rebinding or
-- canonical owner change must never inherit another person's temporary boost.
ALTER TABLE agent_accounts
 ADD COLUMN boost_percent integer,
 ADD COLUMN boost_until timestamptz,
 ADD COLUMN boost_person_id uuid,
 ADD COLUMN boost_link_revision bigint;
