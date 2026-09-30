-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-443: bounded, generation-local progress warning grace period.
SET LOCAL lock_timeout = '5s';
ALTER TABLE harness_sessions ADD COLUMN missing_progress_beats smallint NOT NULL DEFAULT 0;
