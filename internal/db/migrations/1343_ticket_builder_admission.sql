-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1258 expansion: the store serializes builder admission per tenant/ticket
-- with a transaction advisory lock. Legacy writers remain valid; existing
-- parallel sessions are not rewritten or stopped by this expansion.
SET LOCAL lock_timeout = '5s';
ALTER TABLE harness_sessions
    ADD COLUMN parallel_reason text,
    ADD COLUMN started_by_principal_id uuid;
CREATE INDEX harness_active_ticket_builders
    ON harness_sessions (tenant_id,ticket_node_id,created_at,id)
    WHERE role='worker' AND stopped_at IS NULL AND ticket_node_id IS NOT NULL;
