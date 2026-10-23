-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1704: optional extensions to existing tenant/scope protected routines.
-- No existing writer acquires a mandatory field, constraint or launch authority.
SET LOCAL lock_timeout = '5s';
ALTER TABLE recurrence_definitions ADD COLUMN agent_owner_config jsonb;
ALTER TABLE routine_runs ADD COLUMN agent_owner_config jsonb;
ALTER TABLE routine_runs ADD COLUMN agent_result jsonb;
