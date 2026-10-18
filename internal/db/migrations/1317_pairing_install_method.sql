-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-733: the daemon reports how it was installed (homebrew, nix or direct)
-- so Add an account can offer the matching command. The server keeps only
-- values it knows; old writers omit this nullable column and leave it unknown.
ALTER TABLE agent_pairing_computers ADD COLUMN install_method text;
