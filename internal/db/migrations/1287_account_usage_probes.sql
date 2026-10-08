-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-886: expand only; old writers remain valid and every login probe is opt-in.
ALTER TABLE agent_accounts ADD COLUMN usage_probe_enabled boolean;
ALTER TABLE agent_accounts ADD COLUMN usage_probe_revision bigint;
ALTER TABLE agent_accounts ADD COLUMN usage_budget jsonb;
