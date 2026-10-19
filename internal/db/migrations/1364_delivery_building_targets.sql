-- SPDX-License-Identifier: AGPL-3.0-only
-- Expand-only. Nullable Building it targets for one delivery settings scope.
-- NULL inherits the parent scope, or the defaults when none is saved.
-- Existing writers omit the column and keep inheriting.
SET LOCAL lock_timeout = '5s';

ALTER TABLE delivery_settings ADD COLUMN building_targets jsonb;
