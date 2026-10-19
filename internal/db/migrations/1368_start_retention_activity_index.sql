-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1478. Build on the existing audit log without blocking event writers.
-- Candidate checks read immutable activity under the tenant/tree fence rather
-- than acquiring new resource locks from an event trigger after the counter.
CREATE INDEX CONCURRENTLY events_start_retention_refs ON events USING gin(node_refs)
 WHERE metadata->>'imported_from' IS DISTINCT FROM 'start';
