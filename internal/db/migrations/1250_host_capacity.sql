-- SPDX-License-Identifier: AGPL-3.0-only
-- Computer settings and signals contain no credentials or input content.
-- Expand-only: older writers never touch these columns, so constant defaults
-- cover their rows. Bounds (name length, policy/signal shape, 60-point hourly
-- history) are enforced in internal/agentpairing/host_capacity.go and tested
-- there; database CHECKs would be a later contract-phase step.
ALTER TABLE agent_pairing_computers
 ADD COLUMN display_name text NOT NULL DEFAULT '',
 ADD COLUMN capacity_policy jsonb NOT NULL DEFAULT '{"mode":"off","maximum_agents":0,"maximum_load":30,"wait_when_busy":true,"ease_on_battery":true,"ease_when_hot":true,"consider_activity":false}'::jsonb,
 ADD COLUMN capacity_signals jsonb,
 ADD COLUMN capacity_reported_at timestamptz,
 ADD COLUMN capacity_history jsonb NOT NULL DEFAULT '[]'::jsonb;
