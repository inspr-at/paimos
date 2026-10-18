-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
-- Lead decision evidence is immutable even when event RLS hides a referenced
-- ticket after a project move. Uniqueness checks see every indexed row without
-- granting the writer permission to read its payload. Request IDs belong to a
-- tenant/project, not a coordinator generation.
-- This event type is new in AEON-737; earlier released writers never emit it.
-- No existing event is rewritten and other event types remain unrestricted.
-- Use the runner's bounded, resumable concurrent path without blocking writers.
CREATE UNIQUE INDEX CONCURRENTLY events_lead_decision_request_identity
    ON events (tenant_id, node_id, (metadata->>'lead_decision_request_id'))
    WHERE type = 'node.lead_decision_recorded';
