-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-402: a person removes a revoked computer or an account binding from
-- /agents. Removing archives the row: it leaves the lists and the ready count,
-- and keeps its runs, readings and audit events. Nothing is deleted.
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_pairing_computers ADD COLUMN archived_at timestamptz;
ALTER TABLE agent_pairing_computers ADD COLUMN archived_by_principal_id uuid;
ALTER TABLE agent_pairing_computers ADD CONSTRAINT agent_pairing_computers_archive_actor_fk
  FOREIGN KEY (tenant_id, archived_by_principal_id) REFERENCES principals(tenant_id, id);
ALTER TABLE agent_pairing_computers ADD CONSTRAINT agent_pairing_computers_archive_actor_pair
  CHECK ((archived_at IS NULL) = (archived_by_principal_id IS NULL));
-- Only a revoked computer stays archived: live credentials never hide.
ALTER TABLE agent_pairing_computers ADD CONSTRAINT agent_pairing_computers_archive_revoked
  CHECK (archived_at IS NULL OR state = 'revoked');

ALTER TABLE agent_accounts ADD COLUMN archived_at timestamptz;
ALTER TABLE agent_accounts ADD COLUMN archived_by_principal_id uuid;
ALTER TABLE agent_accounts ADD CONSTRAINT agent_accounts_archive_actor_fk
  FOREIGN KEY (tenant_id, archived_by_principal_id) REFERENCES principals(tenant_id, id);
ALTER TABLE agent_accounts ADD CONSTRAINT agent_accounts_archive_actor_pair
  CHECK ((archived_at IS NULL) = (archived_by_principal_id IS NULL));
-- An archived account never routes: it cannot be made available again.
ALTER TABLE agent_accounts ADD CONSTRAINT agent_accounts_archive_unavailable
  CHECK (archived_at IS NULL OR state = 'unavailable');
