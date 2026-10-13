-- SPDX-License-Identifier: AGPL-3.0-only
-- Expand-only; existing writers and tenant RLS are unchanged.
ALTER TABLE delivery_metric_runs ADD COLUMN check_suite_id bigint;
