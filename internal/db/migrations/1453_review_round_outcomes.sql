-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1718: immutable extension of AEON-286, without widening or replacing
-- the released outcome_events kind constraint. Every insert also appends a
-- review_round event in the caller's transaction, after all resource writes.
SET LOCAL lock_timeout = '5s';

CREATE TABLE outcome_review_rounds (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL,
    ticket_node_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('gate', 'design')),
    round integer NOT NULL CHECK (round BETWEEN 1 AND 999),
    verdict text NOT NULL CHECK (verdict IN ('ok', 'changes')),
    cited_rules text[] NOT NULL,
    at timestamptz NOT NULL,
    reviewer_role text NOT NULL CHECK (reviewer_role IN ('reviewer', 'design_reviewer', 'gate_reviewer')),
    artifact text NOT NULL CHECK (char_length(artifact) BETWEEN 1 AND 200),
    review_run_id uuid,
    source text NOT NULL CHECK (source IN ('recorded', 'imported')),
    actor_principal_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 8 AND 200),
    request_digest bytea NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, idempotency_key),
    UNIQUE (tenant_id, ticket_node_id, kind, round),
    FOREIGN KEY (tenant_id, project_id) REFERENCES nodes(tenant_id, id),
    FOREIGN KEY (tenant_id, ticket_node_id) REFERENCES nodes(tenant_id, id),
    FOREIGN KEY (tenant_id, actor_principal_id) REFERENCES principals(tenant_id, id),
    FOREIGN KEY (tenant_id, review_run_id) REFERENCES review_runs(tenant_id, id)
);
CREATE INDEX outcome_review_rounds_project ON outcome_review_rounds (tenant_id, project_id, at, id);
ALTER TABLE outcome_review_rounds ENABLE ROW LEVEL SECURITY;
ALTER TABLE outcome_review_rounds FORCE ROW LEVEL SECURITY;
CREATE POLICY outcome_review_rounds_tenant ON outcome_review_rounds
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY outcome_review_rounds_project ON outcome_review_rounds AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    WITH CHECK ((SELECT aeon_visible_all()) OR project_id = ANY ((SELECT aeon_visible_projects())::uuid[]));
CREATE TRIGGER outcome_review_rounds_immutable BEFORE UPDATE OR DELETE ON outcome_review_rounds
    FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();
