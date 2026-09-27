-- SPDX-License-Identifier: AGPL-3.0-only
-- SC1 / AEON-221: additive activity; existing heartbeat events audit transitions.
ALTER TABLE harness_sessions DROP CONSTRAINT harness_sessions_activity_check;
ALTER TABLE harness_sessions ADD CONSTRAINT harness_sessions_activity_check
    CHECK (activity IN ('unknown', 'busy', 'idle', 'throttled'));
-- State view includes quiet/aging sessions and recent stops; legacy live index stays.
CREATE INDEX harness_sessions_state_recent_idx
    ON harness_sessions (tenant_id, (coalesce(stopped_at, heartbeat_at, created_at)) DESC, id DESC);
