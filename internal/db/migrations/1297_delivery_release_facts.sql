-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1001 expansion: reported release facts carry the release name, its
-- tag and when the live version was healthy again. Nullable columns only;
-- older writers leave them empty.
SET LOCAL lock_timeout = '5s';
ALTER TABLE delivery_metric_marks ADD COLUMN release_name text;
ALTER TABLE delivery_metric_marks ADD COLUMN release_tag text;
ALTER TABLE delivery_metric_marks ADD COLUMN healthy_at timestamptz;
