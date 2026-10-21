-- SPDX-License-Identifier: AGPL-3.0-only
-- Expand only. Old writers may leave these fields NULL; those runs cannot
-- acquire content authority through the paired-computer transport.
ALTER TABLE agent_runs
  ADD COLUMN content_capability_digest bytea,
  ADD COLUMN content_capability_person_id uuid,
  ADD COLUMN content_capability_computer_id uuid,
  ADD COLUMN content_capability_account_id uuid,
  ADD COLUMN content_capability_daemon_id text,
  ADD COLUMN content_capability_generation text,
  ADD COLUMN content_capability_expires_at timestamptz;

CREATE INDEX agent_runs_content_capability_lookup
  ON agent_runs (tenant_id, content_capability_digest)
  WHERE content_capability_digest IS NOT NULL;
