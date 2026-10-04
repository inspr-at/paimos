-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-641 fix4: distinguish undone absence from an explicit default.
-- LEAD reserved 1206–1209; existing rows and audit snapshots remain unchanged.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- An absence marker is valid only for its physical revision. Legacy writers
-- increment revision without knowing this column, automatically saving their
-- new choice. Keep the row and its generation to reject stale/initial CAS.
ALTER TABLE theme_selections ADD COLUMN unsaved_revision bigint;
