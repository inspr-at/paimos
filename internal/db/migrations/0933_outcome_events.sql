-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-286. Append-only objective outcomes of agent work.
-- rules_version stays null until a rules version is recorded (AEON-249).
-- Ticket completion and release inclusion are captured here so every writer
-- is covered. A missing actor skips the row. A visibility failure must not
-- roll back the ticket or membership write.

SET LOCAL lock_timeout = '5s';

CREATE TABLE outcome_events (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    kind text NOT NULL CHECK (kind IN (
        'review_verdict', 'fix_round', 'ci_result', 'revert', 'ticket_done', 'release_included')),
    project_id uuid NOT NULL,
    ticket_node_id uuid NOT NULL,
    session_id uuid,
    rules_version text,
    release_node_id uuid,
    idempotency_key text NOT NULL,
    actor_principal_id uuid NOT NULL,
    source text NOT NULL CHECK (source IN ('recorded', 'automatic')),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 2048),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 16),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    CONSTRAINT outcome_events_idempotency UNIQUE (tenant_id, idempotency_key),
    FOREIGN KEY (tenant_id, actor_principal_id) REFERENCES principals(tenant_id, id),
    FOREIGN KEY (tenant_id, project_id) REFERENCES nodes(tenant_id, id),
    FOREIGN KEY (tenant_id, ticket_node_id) REFERENCES nodes(tenant_id, id),
    FOREIGN KEY (tenant_id, release_node_id) REFERENCES nodes(tenant_id, id),
    CHECK (char_length(idempotency_key) BETWEEN 8 AND 200),
    CHECK (rules_version IS NULL OR (char_length(rules_version) BETWEEN 1 AND 128 AND rules_version !~ '[[:cntrl:]]')),
    CHECK ((kind = 'release_included') = (release_node_id IS NOT NULL))
);

CREATE INDEX outcome_events_ticket_idx ON outcome_events (tenant_id, ticket_node_id, recorded_at DESC, id DESC);
CREATE INDEX outcome_events_session_idx ON outcome_events (tenant_id, session_id, recorded_at DESC, id DESC) WHERE session_id IS NOT NULL;
CREATE INDEX outcome_events_rules_idx ON outcome_events (tenant_id, rules_version, recorded_at DESC, id DESC) WHERE rules_version IS NOT NULL;

ALTER TABLE outcome_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE outcome_events FORCE ROW LEVEL SECURITY;
CREATE POLICY outcome_events_tenant ON outcome_events
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY outcome_events_project_visibility ON outcome_events AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    WITH CHECK ((SELECT aeon_visible_all()) OR project_id = ANY ((SELECT aeon_visible_projects())::uuid[]));
CREATE TRIGGER outcome_events_immutable
    BEFORE UPDATE OR DELETE ON outcome_events
    FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();

CREATE FUNCTION aeon_record_ticket_done() RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, public
AS $$
DECLARE
    actor uuid;
    prior text := '';
BEGIN
    IF TG_OP = 'UPDATE' THEN
        prior := OLD.state;
        IF prior IN ('done', 'accepted', 'delivered') THEN
            RETURN NEW;
        END IF;
    END IF;
    IF NEW.deleted_at IS NOT NULL OR NEW.project_id IS NULL THEN
        RETURN NEW;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM node_kinds k
        WHERE k.tenant_id = NEW.tenant_id AND k.id = NEW.kind_id AND k.slug = 'ticket'
    ) THEN
        RETURN NEW;
    END IF;
    SELECT (aeon_current_principals())[1] INTO actor;
    IF actor IS NULL OR NOT EXISTS (
        SELECT 1 FROM principals p WHERE p.tenant_id = NEW.tenant_id AND p.id = actor
    ) THEN
        RETURN NEW;
    END IF;
    BEGIN
        INSERT INTO outcome_events (
            tenant_id, kind, project_id, ticket_node_id, idempotency_key,
            actor_principal_id, source, payload, request_digest
        ) VALUES (
            NEW.tenant_id,
            'ticket_done',
            NEW.project_id,
            NEW.id,
            'auto:ticket_done:' || NEW.id::text || ':' || clock_timestamp()::text,
            actor,
            'automatic',
            jsonb_build_object('from_state', prior, 'to_state', NEW.state),
            decode(md5('ticket_done' || NEW.id::text || prior || NEW.state || clock_timestamp()::text), 'hex')
        )
        ON CONFLICT (tenant_id, idempotency_key) DO NOTHING;
    EXCEPTION
        WHEN insufficient_privilege THEN
            RETURN NEW;
    END;
    RETURN NEW;
END;
$$;

CREATE TRIGGER outcome_events_ticket_done
    AFTER INSERT OR UPDATE OF state ON nodes
    FOR EACH ROW
    WHEN (NEW.state IN ('done', 'accepted', 'delivered'))
    EXECUTE FUNCTION aeon_record_ticket_done();

CREATE FUNCTION aeon_record_release_included() RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, public
AS $$
DECLARE
    actor uuid;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF OLD.release_node_id IS NOT DISTINCT FROM NEW.release_node_id THEN
            RETURN NEW;
        END IF;
    END IF;
    IF NEW.release_node_id IS NULL OR NEW.project_node_id IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT (aeon_current_principals())[1] INTO actor;
    IF actor IS NULL OR NOT EXISTS (
        SELECT 1 FROM principals p WHERE p.tenant_id = NEW.tenant_id AND p.id = actor
    ) THEN
        RETURN NEW;
    END IF;
    BEGIN
        INSERT INTO outcome_events (
            tenant_id, kind, project_id, ticket_node_id, release_node_id,
            idempotency_key, actor_principal_id, source, payload, request_digest
        ) VALUES (
            NEW.tenant_id,
            'release_included',
            NEW.project_node_id,
            NEW.ticket_node_id,
            NEW.release_node_id,
            'auto:release_included:' || NEW.ticket_node_id::text || ':' || NEW.release_node_id::text || ':' || clock_timestamp()::text,
            actor,
            'automatic',
            jsonb_build_object('release_node_id', NEW.release_node_id),
            decode(md5('release_included' || NEW.ticket_node_id::text || NEW.release_node_id::text || clock_timestamp()::text), 'hex')
        )
        ON CONFLICT (tenant_id, idempotency_key) DO NOTHING;
    EXCEPTION
        WHEN insufficient_privilege THEN
            RETURN NEW;
    END;
    RETURN NEW;
END;
$$;

CREATE TRIGGER outcome_events_release_included
    AFTER INSERT OR UPDATE OF release_node_id ON journey_tickets
    FOR EACH ROW
    WHEN (NEW.release_node_id IS NOT NULL)
    EXECUTE FUNCTION aeon_record_release_included();
