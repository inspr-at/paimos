-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
ALTER TABLE harness_sessions ADD COLUMN work_placement jsonb;
ALTER TABLE agent_runs ADD COLUMN residency text CHECK (residency IN ('eu','local')),
 ADD COLUMN prefs_person_id uuid,
 ADD CONSTRAINT agent_runs_prefs_person_fk FOREIGN KEY (tenant_id,prefs_person_id) REFERENCES principals(tenant_id,id);
CREATE INDEX agent_runs_prefs_person ON agent_runs(tenant_id,prefs_person_id) WHERE prefs_person_id IS NOT NULL;
