-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
ALTER TABLE harness_sessions ADD COLUMN work_placement jsonb;
ALTER TABLE agent_runs ADD COLUMN residency text,
 ADD COLUMN prefs_person_id uuid,
 ADD COLUMN trace jsonb;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_residency_check CHECK (residency IN ('eu','local')) NOT VALID,
 ADD CONSTRAINT agent_runs_prefs_person_fk FOREIGN KEY (tenant_id,prefs_person_id) REFERENCES principals(tenant_id,id) NOT VALID;
ALTER TABLE agent_runs VALIDATE CONSTRAINT agent_runs_residency_check;
ALTER TABLE agent_runs VALIDATE CONSTRAINT agent_runs_prefs_person_fk;
ALTER TABLE work_order_reviews ADD COLUMN trace jsonb;
CREATE INDEX agent_runs_prefs_person ON agent_runs(tenant_id,prefs_person_id) WHERE prefs_person_id IS NOT NULL;
