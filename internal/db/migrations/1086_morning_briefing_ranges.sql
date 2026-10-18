-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-454: date-window outcome reads; events reuse events_at_idx.
-- The concurrent-index runner bounds lock acquisition and repairs invalid
-- indexes on retry while permitting writers throughout the build.
CREATE INDEX CONCURRENTLY IF NOT EXISTS outcomes_briefing_range_idx ON outcome_events (tenant_id, recorded_at DESC, id DESC);
