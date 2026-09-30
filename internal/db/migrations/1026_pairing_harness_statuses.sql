-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-348: runtime readiness is separate from computer setup and ownership.
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_pairing_computers
    ADD COLUMN harness_statuses jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(harness_statuses) = 'object');
