-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-286. Append-only objective outcomes of agent work.
-- rules_version stays null until a rules version is recorded (AEON-249).
-- Ticket completion and release publication are captured here so every writer
-- is covered. Assigning a ticket to a planned release records nothing.
-- Completion stores the interval from the earliest worker-marker start, else
-- that marker's comment time, else the first in-progress transition. A future
-- start omits elapsed_seconds. Automatic rows copy the ticket's harness
-- session, or a UUID from the latest worker marker.
-- A missing actor skips the row. A visibility failure must not roll back
-- the ticket write or the snapshot capture.
-- Automatic keys are stable: one completion per ticket, and one publication
-- per ticket, release and version. A second capture of that identity does nothing.

SET LOCAL lock_timeout = '5s';

CREATE TABLE outcome_events (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    kind text NOT NULL CHECK (kind IN (
        'review_verdict', 'fix_round', 'ci_result', 'revert', 'ticket_done', 'released')),
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
    CHECK ((kind = 'released') = (release_node_id IS NOT NULL))
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

CREATE FUNCTION aeon_outcome_timestamptz(raw text) RETURNS timestamptz
LANGUAGE plpgsql
STABLE
SECURITY INVOKER
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF raw IS NULL OR btrim(raw) = '' THEN
        RETURN NULL;
    END IF;
    RETURN btrim(raw)::timestamptz;
EXCEPTION
    WHEN invalid_datetime_format OR datetime_field_overflow THEN
        RETURN NULL;
END;
$$;

-- A worker marker is the attribution comment. started: is read case-insensitively
-- and trailing sentence punctuation is ignored, matching the activity parser.
CREATE FUNCTION aeon_outcome_marker(body text) RETURNS boolean
LANGUAGE sql
STABLE
SET search_path = pg_catalog, public
AS $$
    SELECT coalesce(body, '') ~* '^I work on this[[:space:]]*[-—–]+[[:space:]]*session:'
       AND substring(lower(body) FROM 'started:[[:space:]]*([^[:space:]]+)') IS NOT NULL;
$$;

CREATE FUNCTION aeon_outcome_work_started(p_tenant uuid, p_ticket uuid) RETURNS timestamptz
LANGUAGE plpgsql
STABLE
SECURITY INVOKER
SET search_path = pg_catalog, public
AS $$
DECLARE
    parsed timestamptz;
    marker_at timestamptz;
BEGIN
    SELECT min(aeon_outcome_timestamptz(regexp_replace(
        substring(lower(e.after->>'body_markdown') FROM 'started:[[:space:]]*([^[:space:]]+)'),
        '[.;,]+$', '')))
    INTO parsed
    FROM events e
    WHERE e.tenant_id = p_tenant
      AND e.node_id = p_ticket
      AND e.type = 'comment.created'
      AND aeon_outcome_marker(e.after->>'body_markdown');
    IF parsed IS NOT NULL THEN
        RETURN parsed;
    END IF;
    SELECT min(e.at) INTO marker_at
    FROM events e
    WHERE e.tenant_id = p_tenant
      AND e.node_id = p_ticket
      AND e.type = 'comment.created'
      AND aeon_outcome_marker(e.after->>'body_markdown');
    IF marker_at IS NOT NULL THEN
        RETURN marker_at;
    END IF;
    SELECT min(e.at) INTO marker_at
    FROM events e
    WHERE e.tenant_id = p_tenant
      AND e.node_id = p_ticket
      AND e.type IN ('node.created', 'node.updated')
      AND e.after->>'state' = 'in_progress';
    RETURN marker_at;
END;
$$;

CREATE FUNCTION aeon_outcome_session(p_tenant uuid, p_ticket uuid) RETURNS uuid
LANGUAGE plpgsql
STABLE
SECURITY INVOKER
SET search_path = pg_catalog, public
AS $$
DECLARE
    found uuid;
    raw text;
BEGIN
    SELECT s.id INTO found
    FROM harness_sessions s
    WHERE s.tenant_id = p_tenant AND s.ticket_node_id = p_ticket
    ORDER BY (s.stopped_at IS NULL) DESC, s.heartbeat_at DESC NULLS LAST, s.created_at DESC, s.id DESC
    LIMIT 1;
    IF found IS NOT NULL THEN
        RETURN found;
    END IF;
    SELECT substring(lower(e.after->>'body_markdown') FROM 'session:[[:space:]]*[^()[:cntrl:]]{0,200}\(([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\)')
    INTO raw
    FROM events e
    WHERE e.tenant_id = p_tenant
      AND e.node_id = p_ticket
      AND e.type = 'comment.created'
      AND aeon_outcome_marker(e.after->>'body_markdown')
    ORDER BY e.at DESC, e.id DESC
    LIMIT 1;
    IF raw IS NULL OR raw = '' THEN
        RETURN NULL;
    END IF;
    RETURN raw::uuid;
EXCEPTION
    WHEN invalid_text_representation THEN
        RETURN NULL;
END;
$$;

CREATE FUNCTION aeon_record_ticket_done() RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, public
AS $$
DECLARE
    actor uuid;
    prior text := '';
    started timestamptz;
    seconds numeric;
    work_session uuid;
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
    started := aeon_outcome_work_started(NEW.tenant_id, NEW.id);
    work_session := aeon_outcome_session(NEW.tenant_id, NEW.id);
    IF started IS NOT NULL AND started <= clock_timestamp() THEN
        seconds := floor(extract(epoch FROM clock_timestamp() - started));
    END IF;
    BEGIN
        INSERT INTO outcome_events (
            tenant_id, kind, project_id, ticket_node_id, session_id, idempotency_key,
            actor_principal_id, source, payload, request_digest
        ) VALUES (
            NEW.tenant_id,
            'ticket_done',
            NEW.project_id,
            NEW.id,
            work_session,
            'auto:ticket_done:' || NEW.id::text,
            actor,
            'automatic',
            jsonb_strip_nulls(jsonb_build_object(
                'from_state', prior,
                'to_state', NEW.state,
                'started_at', started,
                'elapsed_seconds', seconds
            )),
            decode(md5('auto:ticket_done:' || NEW.id::text), 'hex')
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

-- Publication is the release-note snapshot insert: the journey freeze trigger
-- writes one when a release becomes released, and a historical manifest
-- capture writes the other. Membership edits before that insert nothing.
CREATE FUNCTION aeon_record_released_snapshot() RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, public
AS $$
DECLARE
    actor uuid;
    release_id uuid;
    version text;
    scheme text;
    clean_version text;
    ticket jsonb;
    ticket_id uuid;
    work_session uuid;
    outcome_key text;
BEGIN
    SELECT (aeon_current_principals())[1] INTO actor;
    IF actor IS NULL OR NOT EXISTS (
        SELECT 1 FROM principals p WHERE p.tenant_id = NEW.tenant_id AND p.id = actor
    ) THEN
        RETURN NEW;
    END IF;
    IF TG_TABLE_NAME = 'journey_release_note_snapshots' THEN
        release_id := NEW.release_node_id;
        version := NEW.snapshot->>'version';
        scheme := NEW.snapshot->>'version_scheme';
    ELSE
        BEGIN
            release_id := NULLIF(NEW.snapshot->>'release_node_id', '')::uuid;
        EXCEPTION
            WHEN invalid_text_representation THEN
                RETURN NEW;
        END;
        version := coalesce(NEW.snapshot->>'version', NEW.version);
        scheme := NEW.snapshot->>'version_scheme';
    END IF;
    IF release_id IS NULL OR jsonb_typeof(NEW.snapshot->'tickets') IS DISTINCT FROM 'array' THEN
        RETURN NEW;
    END IF;
    clean_version := NULLIF(btrim(coalesce(version, '')), '');
    FOR ticket IN
        SELECT value FROM jsonb_array_elements(NEW.snapshot->'tickets')
    LOOP
        BEGIN
            ticket_id := NULLIF(ticket->>'id', '')::uuid;
        EXCEPTION
            WHEN invalid_text_representation THEN
                CONTINUE;
        END;
        IF ticket_id IS NULL OR NOT EXISTS (
            SELECT 1 FROM nodes n
            WHERE n.tenant_id = NEW.tenant_id AND n.id = ticket_id AND n.project_id = NEW.project_node_id
        ) THEN
            CONTINUE;
        END IF;
        work_session := aeon_outcome_session(NEW.tenant_id, ticket_id);
        outcome_key := 'auto:released:' || ticket_id::text || ':' || release_id::text || ':' || coalesce(clean_version, '');
        IF char_length(outcome_key) > 200 THEN
            CONTINUE;
        END IF;
        BEGIN
            INSERT INTO outcome_events (
                tenant_id, kind, project_id, ticket_node_id, session_id, release_node_id,
                idempotency_key, actor_principal_id, source, payload, request_digest
            ) VALUES (
                NEW.tenant_id,
                'released',
                NEW.project_node_id,
                ticket_id,
                work_session,
                release_id,
                outcome_key,
                actor,
                'automatic',
                jsonb_strip_nulls(jsonb_build_object(
                    'version', clean_version,
                    'version_scheme', NULLIF(btrim(coalesce(scheme, '')), '')
                )),
                decode(md5(outcome_key), 'hex')
            )
            ON CONFLICT (tenant_id, idempotency_key) DO NOTHING;
        EXCEPTION
            WHEN insufficient_privilege THEN
                CONTINUE;
        END;
    END LOOP;
    RETURN NEW;
END;
$$;

CREATE TRIGGER outcome_events_released_snapshot
    AFTER INSERT ON journey_release_note_snapshots
    FOR EACH ROW
    EXECUTE FUNCTION aeon_record_released_snapshot();
