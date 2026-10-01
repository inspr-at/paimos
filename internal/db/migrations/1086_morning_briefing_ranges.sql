-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-454: date-window reads reuse the existing logs, without new tracking.
SET LOCAL lock_timeout = '5s';
CREATE INDEX events_briefing_range_idx ON events (tenant_id, at, id);
CREATE INDEX outcomes_briefing_range_idx ON outcome_events (tenant_id, recorded_at DESC, id DESC);
