-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1478. Build on the existing audit log without blocking event writers.
-- Positive import provenance supports the worker's bounded source backfill.
CREATE INDEX CONCURRENTLY events_start_import_projects ON events(tenant_id,node_id)
 WHERE type='node.created' AND metadata->>'imported_from'='start';
