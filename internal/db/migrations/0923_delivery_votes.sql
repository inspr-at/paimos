-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-218. A person rates one harness session. The row keeps the ticket and a
-- snapshot of that session's model, harness and account. Agents cannot write.
SET LOCAL lock_timeout = '5s';

CREATE TABLE agent_delivery_votes (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    session_id uuid NOT NULL,
    ticket_node_id uuid NOT NULL,
    voter_principal_id uuid NOT NULL,
    score smallint NOT NULL CHECK (score BETWEEN 1 AND 5),
    tags text[] NOT NULL DEFAULT '{}',
    comment text NOT NULL DEFAULT '',
    harness text NOT NULL CHECK (char_length(harness) BETWEEN 1 AND 32 AND harness = btrim(harness) AND harness ~ '^[a-z][a-z0-9_-]*$'),
    model text CHECK (model IS NULL OR (char_length(model) BETWEEN 1 AND 120 AND model = btrim(model) AND model !~ '[[:cntrl:]]')),
    account_label text CHECK (account_label IS NULL OR (char_length(account_label) BETWEEN 1 AND 60 AND account_label = btrim(account_label) AND account_label !~ '[[:cntrl:]]')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, session_id, voter_principal_id),
    FOREIGN KEY (tenant_id, session_id) REFERENCES harness_sessions(tenant_id, id),
    FOREIGN KEY (tenant_id, ticket_node_id) REFERENCES nodes(tenant_id, id),
    FOREIGN KEY (tenant_id, voter_principal_id) REFERENCES principals(tenant_id, id),
    CHECK (cardinality(tags) <= 3),
    CHECK (tags <@ ARRAY['quality', 'rework', 'taste']::text[]),
    CHECK (char_length(comment) <= 2000),
    CHECK (translate(comment, chr(10) || chr(9), '') !~ '[[:cntrl:]]')
);
CREATE INDEX agent_delivery_votes_ticket_idx ON agent_delivery_votes (tenant_id, ticket_node_id);
ALTER TABLE agent_delivery_votes ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_delivery_votes FORCE ROW LEVEL SECURITY;
CREATE POLICY agent_delivery_votes_tenant ON agent_delivery_votes
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
COMMENT ON TABLE agent_delivery_votes IS 'One person''s rating of one harness session. Model, harness and account are copied from the session at save time.';

CREATE FUNCTION aeon_delivery_vote_person() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND (NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
        OR NEW.session_id IS DISTINCT FROM OLD.session_id
        OR NEW.voter_principal_id IS DISTINCT FROM OLD.voter_principal_id
        OR NEW.id IS DISTINCT FROM OLD.id) THEN
        RAISE EXCEPTION 'a delivery vote keeps its voter and session' USING ERRCODE = '42501';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM principals p
        WHERE p.tenant_id = NEW.tenant_id AND p.id = NEW.voter_principal_id
          AND p.kind = 'person' AND p.status = 'active'
    ) THEN
        RAISE EXCEPTION 'delivery votes are limited to active people' USING ERRCODE = '42501';
    END IF;
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END;
$$;
REVOKE EXECUTE ON FUNCTION aeon_delivery_vote_person() FROM PUBLIC;

CREATE TRIGGER agent_delivery_votes_person
    BEFORE INSERT OR UPDATE ON agent_delivery_votes
    FOR EACH ROW EXECUTE FUNCTION aeon_delivery_vote_person();
