-- SPDX-License-Identifier: AGPL-3.0-only
-- Expand only: previous binaries must still be able to write creatorless keys.
-- All current creation paths require an active, same-tenant person in code.
-- Adoption records this marker; ownership constraints belong to a later
-- contract phase after this expansion has shipped (AEON-724, release 124).
ALTER TABLE agent_keys ADD COLUMN person_owner_required boolean DEFAULT false;
