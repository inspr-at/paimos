-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1588: NULL and an absent settings row both preserve today's writers.
SET LOCAL lock_timeout = '5s';
ALTER TABLE rule_budget_settings ADD COLUMN db_authoring boolean;
