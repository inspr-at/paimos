-- SPDX-License-Identifier: AGPL-3.0-only
-- Expand-only; delivery_metric_sources retains its existing tenant/project RLS.
ALTER TABLE delivery_metric_sources
    ADD COLUMN last_read_error jsonb,
    ADD COLUMN last_event_at timestamptz,
    ADD COLUMN backfill_retry_at timestamptz,
    ADD COLUMN backfill_pending boolean,
    ADD COLUMN backfill_actor_id uuid,
    ADD COLUMN backfill_key_id uuid,
    ADD COLUMN backfill_days integer;
