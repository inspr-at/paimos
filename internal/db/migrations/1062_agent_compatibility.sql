-- SPDX-License-Identifier: AGPL-3.0-only
-- Advisory helper identity, reported under the existing lifecycle proof.
-- Empty identity is unknown, including for legacy helpers after a downgrade.
ALTER TABLE agent_pairing_computers
  ADD COLUMN agent_protocol text NOT NULL DEFAULT '' CHECK (length(agent_protocol) <= 64),
  ADD COLUMN agent_version text NOT NULL DEFAULT '' CHECK (length(agent_version) <= 64),
  ADD COLUMN agent_version_scheme text NOT NULL DEFAULT '' CHECK (length(agent_version_scheme) <= 64);
