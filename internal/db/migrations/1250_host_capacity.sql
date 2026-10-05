-- SPDX-License-Identifier: AGPL-3.0-only
-- Computer settings and signals contain no credentials or input content.
ALTER TABLE agent_pairing_computers
 ADD COLUMN display_name text NOT NULL DEFAULT '' CHECK (length(display_name)<=128),
 ADD COLUMN capacity_policy jsonb NOT NULL DEFAULT '{"mode":"off","maximum_agents":0,"maximum_load":30,"wait_when_busy":true,"ease_on_battery":true,"ease_when_hot":true,"consider_activity":false}'::jsonb,
 ADD COLUMN capacity_signals jsonb,
 ADD COLUMN capacity_reported_at timestamptz,
 ADD COLUMN capacity_history jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE agent_pairing_computers
 ADD CONSTRAINT host_capacity_history_bounded CHECK (jsonb_typeof(capacity_history)='array' AND jsonb_array_length(capacity_history)<=60),
 ADD CONSTRAINT host_capacity_policy_bounded CHECK (octet_length(capacity_policy::text)<=2048),
 ADD CONSTRAINT host_capacity_signals_bounded CHECK (octet_length(capacity_signals::text)<=2048);
