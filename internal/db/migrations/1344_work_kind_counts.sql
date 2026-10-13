-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1230: tenant-scoped area counts over live nodes, under existing FORCE RLS.
-- Expand-only index; no new tables or columns require DSAR classification.
-- The runner provides a 30-minute build budget and a 5-second lock budget.
CREATE INDEX CONCURRENTLY IF NOT EXISTS nodes_work_kind_area_idx ON nodes (tenant_id, (fields->>'area'))
    WHERE deleted_at IS NULL AND fields->>'area' IS NOT NULL;
