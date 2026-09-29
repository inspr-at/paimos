-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-329: the ticket list sums tokens and cost over every session of a
-- ticket, stopped ones included, and sorts by them. harness_sessions_ticket_eta
-- covers running sessions only. harness_sessions is small, so a plain CREATE
-- INDEX under lock_timeout is acceptable inside this migration transaction.
SET LOCAL lock_timeout = '5s';
CREATE INDEX harness_sessions_ticket_node ON harness_sessions (tenant_id, ticket_node_id)
    WHERE ticket_node_id IS NOT NULL;
