-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1133: scan delivery events without walking the unrelated event tail.
-- Keep the predicate identical to flowSourceEventsSQL in delivery/flow_paimos.go.
-- The concurrent migration runner bounds lock acquisition to 5s and restores it.
-- IF NOT EXISTS preserves the matching index already installed by OPS.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_delivery_flow_idx ON events (tenant_id, id)
    WHERE type LIKE 'delivery.work\_queue.%' OR type LIKE 'delivery.review.%' OR type='delivery.state_changed';
