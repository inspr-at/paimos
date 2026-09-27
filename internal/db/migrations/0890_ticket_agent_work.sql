-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-216: bound ticket and epic agent-work lookups by the session's ticket.
CREATE INDEX harness_sessions_ticket_work_idx
    ON harness_sessions (tenant_id, ticket_node_id)
    WHERE ticket_node_id IS NOT NULL;
