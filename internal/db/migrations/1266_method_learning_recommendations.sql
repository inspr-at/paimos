-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-788: an agent's (or a person's) recommendation for one open method
-- learning: accept with a lesson line into a knowledge entry, or dismiss with
-- a reason. It never decides anything; a person applies it through the
-- existing accept and dismiss paths. One current recommendation per learning,
-- replaced in place and recorded as knowledge.learning_recommended in the same
-- transaction. learning_text is the learning as the recommender saw it, so a
-- later edit of the source marks the recommendation as stale.
SET LOCAL lock_timeout = '5s';

CREATE TABLE method_learning_recommendations (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    source_key text NOT NULL,
    project_id uuid NOT NULL,
    decision text NOT NULL CHECK (decision IN ('accept', 'dismiss')),
    knowledge_id uuid,
    lesson text,
    reason text,
    learning_text text NOT NULL,
    recommended_by uuid NOT NULL,
    recommended_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    event_id bigint NOT NULL,
    PRIMARY KEY (tenant_id, source_key),
    FOREIGN KEY (tenant_id, project_id) REFERENCES nodes (tenant_id, id),
    FOREIGN KEY (tenant_id, knowledge_id) REFERENCES nodes (tenant_id, id),
    FOREIGN KEY (tenant_id, recommended_by) REFERENCES principals (tenant_id, id),
    FOREIGN KEY (tenant_id, event_id) REFERENCES events (tenant_id, id),
    CHECK (source_key ~ '^n-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        OR source_key ~ '^c-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}-[1-9][0-9]*$'),
    CHECK ((decision = 'accept') = (knowledge_id IS NOT NULL)),
    CHECK (decision = 'accept' OR (lesson IS NULL AND reason IS NOT NULL))
);

CREATE INDEX method_learning_recommendations_project_idx
    ON method_learning_recommendations (tenant_id, project_id);

ALTER TABLE method_learning_recommendations ENABLE ROW LEVEL SECURITY;
ALTER TABLE method_learning_recommendations FORCE ROW LEVEL SECURITY;
CREATE POLICY method_learning_recommendations_tenant ON method_learning_recommendations
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
