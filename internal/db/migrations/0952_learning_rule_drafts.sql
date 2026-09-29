-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-288: a person can turn a method learning into a rule draft, and a
-- daily job can nominate candidates. Nothing here publishes a rule.
SET LOCAL lock_timeout = '5s';

ALTER TABLE method_learning_decisions
    ADD COLUMN rule_set_id uuid,
    ADD COLUMN rule_layer_id uuid,
    ADD COLUMN rule_identity text;

-- The decision checks name "decision". The source_key check does not, and stays.
DO $$
DECLARE
    drop_name text;
BEGIN
    FOR drop_name IN
        SELECT con.conname
        FROM pg_constraint con
        WHERE con.conrelid = 'method_learning_decisions'::regclass
          AND con.contype = 'c'
          AND pg_get_constraintdef(con.oid) LIKE '%decision%'
    LOOP
        EXECUTE format('ALTER TABLE method_learning_decisions DROP CONSTRAINT %I', drop_name);
    END LOOP;
END $$;

ALTER TABLE method_learning_decisions
    ADD CONSTRAINT method_learning_decisions_decision_check
        CHECK (decision IN ('accepted', 'dismissed', 'drafted')),
    ADD CONSTRAINT method_learning_decisions_shape_check
        CHECK (
            (decision = 'accepted'
                AND knowledge_id IS NOT NULL AND line IS NOT NULL AND heading IS NOT NULL
                AND rule_set_id IS NULL AND rule_layer_id IS NULL AND rule_identity IS NULL)
            OR (decision = 'dismissed'
                AND knowledge_id IS NULL AND line IS NULL AND heading IS NULL
                AND rule_set_id IS NULL AND rule_layer_id IS NULL AND rule_identity IS NULL)
            OR (decision = 'drafted'
                AND knowledge_id IS NULL AND line IS NULL AND heading IS NULL
                AND rule_set_id IS NOT NULL AND rule_layer_id IS NOT NULL AND rule_identity IS NOT NULL
                AND rule_identity ~ '^[a-z][a-z0-9._-]{0,95}$')
        ),
    ADD CONSTRAINT method_learning_decisions_rule_set_fk
        FOREIGN KEY (tenant_id, rule_set_id) REFERENCES nodes (tenant_id, id),
    ADD CONSTRAINT method_learning_decisions_rule_layer_fk
        FOREIGN KEY (tenant_id, rule_layer_id) REFERENCES nodes (tenant_id, id);

-- Nominations are how the daily tagger shows a candidate that has no
-- process-learning tag yet (a review verdict or an incident comment).
-- A decision still hides it. The tagger never inserts a decision.
CREATE TABLE method_learning_nominations (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    source_key text NOT NULL,
    project_id uuid NOT NULL,
    node_id uuid NOT NULL,
    comment_id bigint,
    origin text NOT NULL CHECK (origin IN ('closed_ticket', 'review_verdict', 'incident_comment')),
    excerpt text NOT NULL CHECK (char_length(excerpt) BETWEEN 1 AND 240),
    -- sha256 of the source text the excerpt came from. A reader re-derives
    -- the text from the live source and serves the stored excerpt only while
    -- the hash still matches, so an edited source never shows the old copy.
    source_hash text NOT NULL CHECK (source_hash ~ '^[0-9a-f]{64}$'),
    -- A review verdict from outcome_events keeps that record's own id and
    -- project: project_id is then the verdict's project, not the ticket's.
    outcome_id text CHECK (outcome_id IS NULL OR char_length(outcome_id) BETWEEN 1 AND 64),
    nominated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, source_key),
    FOREIGN KEY (tenant_id, project_id) REFERENCES nodes (tenant_id, id),
    FOREIGN KEY (tenant_id, node_id) REFERENCES nodes (tenant_id, id),
    CHECK (source_key ~ '^n-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        OR source_key ~ '^c-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}-[1-9][0-9]*$'),
    CHECK ((comment_id IS NULL) = (source_key LIKE 'n-%')),
    CHECK (
        (origin = 'closed_ticket' AND comment_id IS NULL)
        OR (origin = 'incident_comment' AND comment_id IS NOT NULL)
        OR origin = 'review_verdict'
    ),
    CHECK (outcome_id IS NULL OR (origin = 'review_verdict' AND comment_id IS NULL))
);

CREATE INDEX method_learning_nominations_project_idx
    ON method_learning_nominations (tenant_id, project_id);

ALTER TABLE method_learning_nominations ENABLE ROW LEVEL SECURITY;
ALTER TABLE method_learning_nominations FORCE ROW LEVEL SECURITY;
CREATE POLICY method_learning_nominations_tenant ON method_learning_nominations
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

-- One cursor per source. Null means that source has never been scanned.
-- The cursor is the (timestamp, id) of the last row read, exclusive, so a
-- batch of rows that share one timestamp cannot stall the scan. A null id
-- after a timestamp means everything at or before that timestamp was read.
CREATE TABLE method_learning_tag_cursor (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
    tickets_until timestamptz,
    tickets_after_id text,
    comments_until timestamptz,
    comments_after_id text,
    verdicts_until timestamptz,
    verdicts_after_id text
);

ALTER TABLE method_learning_tag_cursor ENABLE ROW LEVEL SECURITY;
ALTER TABLE method_learning_tag_cursor FORCE ROW LEVEL SECURITY;
CREATE POLICY method_learning_tag_cursor_tenant ON method_learning_tag_cursor
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
