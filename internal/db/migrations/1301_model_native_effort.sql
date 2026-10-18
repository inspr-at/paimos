-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
-- Legacy thinking remains available during the additive transition.
ALTER TABLE model_pref_orders ADD COLUMN effort text, ADD COLUMN effort_level smallint;
