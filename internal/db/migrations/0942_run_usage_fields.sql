-- SPDX-License-Identifier: AGPL-3.0-only
-- Additive per-run usage fields (AEON-300). Existing rows stay null.
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_runs ADD COLUMN active_ms bigint
  CHECK (active_ms IS NULL OR active_ms BETWEEN 0 AND 1000000000000);
ALTER TABLE agent_runs ADD COLUMN outcome_detail text
  CHECK (outcome_detail IS NULL OR outcome_detail IN ('no_commit','committed','pr_opened','merged','abandoned'));
ALTER TABLE agent_runs ADD COLUMN retry_of_run_id uuid;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_retry_of_fk
  FOREIGN KEY (tenant_id, retry_of_run_id) REFERENCES agent_runs (tenant_id, id);
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_retry_not_self
  CHECK (retry_of_run_id IS NULL OR retry_of_run_id <> id);
CREATE INDEX agent_runs_retry_idx ON agent_runs (tenant_id, retry_of_run_id) WHERE retry_of_run_id IS NOT NULL;
ALTER TABLE run_telemetry ADD COLUMN status text
  CHECK (status IS NULL OR status IN ('starting','running','waiting','completed','failed','cancelled','ownership_lost'));
