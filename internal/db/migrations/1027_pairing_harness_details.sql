-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-348: additive fixed reason codes and recovery commands per harness.
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_pairing_computers
    ADD COLUMN harness_details jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(harness_details) = 'object');
