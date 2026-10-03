-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-450: exact commit-range reviews, routed through ordinary managed runs.
SET LOCAL lock_timeout = '5s';

ALTER TABLE work_orders ADD COLUMN kind text NOT NULL DEFAULT 'build'
    CHECK (kind IN ('build', 'review'));

CREATE TABLE work_order_reviews (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    work_order_id uuid NOT NULL,
    ticket_node_id uuid NOT NULL,
    request_id uuid NOT NULL,
    request jsonb NOT NULL,
    ticket_snapshot text NOT NULL CHECK (octet_length(ticket_snapshot)<=131072),
    repository text NOT NULL CHECK (repository ~ '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' AND length(repository)<=200),
    base_sha text NOT NULL CHECK (base_sha ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    head_sha text NOT NULL CHECK (head_sha ~ '^([0-9a-f]{40}|[0-9a-f]{64})$' AND head_sha<>base_sha),
    author_run_id uuid,
    author_family text NOT NULL CHECK (author_family IN ('openai','anthropic','xai','cursor')),
    reviewer_profile_id uuid,
    reviewer_family text CHECK (reviewer_family IN ('openai','anthropic','xai','cursor') AND reviewer_family<>author_family),
    run_id uuid,
    pull_request bigint CHECK (pull_request>0),
    ladder jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(ladder)='array'),
    result jsonb NOT NULL DEFAULT '{"verdict":"","findings":[],"reason":"Review has not returned a verdict."}'::jsonb,
    evidence_id uuid,
    github_status text NOT NULL DEFAULT 'unconfigured'
        CHECK (github_status IN ('unconfigured','pending','success','failure','error','stale')),
    github_reported_state text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, work_order_id),
    UNIQUE (tenant_id, request_id),
    UNIQUE (tenant_id, run_id),
    FOREIGN KEY (tenant_id, work_order_id) REFERENCES work_orders(tenant_id,node_id),
    FOREIGN KEY (tenant_id, ticket_node_id) REFERENCES nodes(tenant_id,id),
    FOREIGN KEY (tenant_id, author_run_id) REFERENCES agent_runs(tenant_id,id),
    FOREIGN KEY (tenant_id, reviewer_profile_id) REFERENCES model_profiles(tenant_id,id),
    FOREIGN KEY (tenant_id, work_order_id, run_id) REFERENCES agent_runs(tenant_id,work_order_id,id),
    FOREIGN KEY (tenant_id, evidence_id) REFERENCES work_evidence(tenant_id,id),
    CHECK ((reviewer_profile_id IS NULL) = (reviewer_family IS NULL))
);
CREATE INDEX work_order_reviews_ticket ON work_order_reviews(tenant_id,ticket_node_id,created_at DESC,work_order_id);
ALTER TABLE work_order_reviews ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_order_reviews FORCE ROW LEVEL SECURITY;
CREATE POLICY review_tenant ON work_order_reviews
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY review_project ON work_order_reviews AS RESTRICTIVE
    USING (EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=work_order_reviews.tenant_id AND n.id=ticket_node_id AND n.deleted_at IS NULL))
    WITH CHECK (EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=work_order_reviews.tenant_id AND n.id=ticket_node_id AND n.deleted_at IS NULL));

-- Bindings and the first verdict survive all later run/status projections.
CREATE FUNCTION aeon_review_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.tenant_id,NEW.work_order_id,NEW.ticket_node_id,NEW.request_id,NEW.request,
        NEW.ticket_snapshot,NEW.repository,NEW.base_sha,NEW.head_sha,NEW.author_run_id,NEW.author_family,
        NEW.reviewer_profile_id,NEW.reviewer_family,NEW.pull_request,NEW.ladder,NEW.created_at)
       IS DISTINCT FROM
       (OLD.tenant_id,OLD.work_order_id,OLD.ticket_node_id,OLD.request_id,OLD.request,
        OLD.ticket_snapshot,OLD.repository,OLD.base_sha,OLD.head_sha,OLD.author_run_id,OLD.author_family,
        OLD.reviewer_profile_id,OLD.reviewer_family,OLD.pull_request,OLD.ladder,OLD.created_at)
       OR (OLD.run_id IS NOT NULL AND NEW.run_id IS DISTINCT FROM OLD.run_id)
       OR (OLD.evidence_id IS NOT NULL AND (NEW.evidence_id,NEW.result) IS DISTINCT FROM (OLD.evidence_id,OLD.result)) THEN
        RAISE EXCEPTION 'review bindings and verdicts are immutable' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER review_immutable BEFORE UPDATE ON work_order_reviews
    FOR EACH ROW EXECUTE FUNCTION aeon_review_immutable();
