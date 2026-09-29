-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

-- Existing forced tenant/project RLS applies to these lineage columns too.
ALTER TABLE harness_sessions
    ADD COLUMN handed_over_to_id uuid,
    ADD COLUMN adopted_from_id uuid,
    ADD CONSTRAINT harness_successor_project FOREIGN KEY (tenant_id,project_id,handed_over_to_id)
        REFERENCES harness_sessions(tenant_id,project_id,id),
    ADD CONSTRAINT harness_adoption_project FOREIGN KEY (tenant_id,project_id,adopted_from_id)
        REFERENCES harness_sessions(tenant_id,project_id,id),
    ADD CONSTRAINT harness_successor_not_self CHECK (handed_over_to_id <> id),
    ADD CONSTRAINT harness_adoption_not_self CHECK (adopted_from_id <> id);
