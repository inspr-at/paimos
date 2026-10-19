-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1354. Build without blocking event writers on the existing large log.
-- The predicate is the prune rule from 1369: kept heartbeats never enter the
-- index, so each batch reads only prunable rows, however many state changes
-- accumulate before the cutoff.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_telemetry_retention_idx
 ON events(tenant_id,at,id) WHERE aeon_telemetry_prunable(type,before,after);
