-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1354. Build without blocking event writers on the existing large log.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_telemetry_retention_idx
 ON events(tenant_id,at,id) WHERE type IN ('account.probed','harness.heartbeat');
