-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-329: include stopped sessions when loading ticket usage in bulk.
-- The concurrent-index runner sets lock_timeout to 5s and repairs invalid
-- indexes on retry, without blocking session writes during construction.
CREATE INDEX CONCURRENTLY IF NOT EXISTS harness_sessions_ticket_node ON harness_sessions (tenant_id, ticket_node_id)
    WHERE ticket_node_id IS NOT NULL;
