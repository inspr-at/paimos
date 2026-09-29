-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-275: a person's accept or dismiss of a method learning. Open candidates
-- are not stored; they are tickets, tasks and epics tagged process-learning,
-- and comments that carry that tag. This row is the decision, written in the
-- same transaction as its event. Undo deletes the row.
SET LOCAL lock_timeout = '5s';

CREATE TABLE method_learning_decisions (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL,
    source_key text NOT NULL,
    decision text NOT NULL CHECK (decision IN ('accepted', 'dismissed')),
    knowledge_id uuid,
    heading text,
    line text,
    decided_by uuid NOT NULL,
    decided_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    event_id bigint NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, source_key),
    UNIQUE (tenant_id, event_id),
    FOREIGN KEY (tenant_id, project_id) REFERENCES nodes (tenant_id, id),
    FOREIGN KEY (tenant_id, knowledge_id) REFERENCES nodes (tenant_id, id),
    FOREIGN KEY (tenant_id, decided_by) REFERENCES principals (tenant_id, id),
    FOREIGN KEY (tenant_id, event_id) REFERENCES events (tenant_id, id),
    CHECK (source_key ~ '^n-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        OR source_key ~ '^c-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}-[1-9][0-9]*$'),
    CHECK ((decision = 'accepted') = (knowledge_id IS NOT NULL AND line IS NOT NULL AND heading IS NOT NULL))
);

CREATE INDEX method_learning_decisions_project_idx
    ON method_learning_decisions (tenant_id, project_id);

ALTER TABLE method_learning_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE method_learning_decisions FORCE ROW LEVEL SECURITY;
CREATE POLICY method_learning_decisions_tenant ON method_learning_decisions
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
