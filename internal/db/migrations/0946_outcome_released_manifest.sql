-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-286. release_manifest_note_snapshots arrives in 0939, after the
-- outcome function in 0933, so this trigger is created once that table exists.
-- A historical manifest capture records the same released outcome as publication.
-- Git-tag releases have no journey release node. Their identity is the tenant,
-- the project and the version. 0933 skipped that shape; this replaces the function.

SET LOCAL lock_timeout = '5s';

DO $$
DECLARE
    cname text;
BEGIN
    SELECT con.conname INTO cname
    FROM pg_constraint con
    WHERE con.conrelid = 'outcome_events'::regclass
      AND con.contype = 'c'
      AND pg_get_constraintdef(con.oid) LIKE '%kind = ''released''%'
      AND pg_get_constraintdef(con.oid) LIKE '%release_node_id IS NOT NULL%'
      AND pg_get_constraintdef(con.oid) NOT LIKE '%payload%';
    IF cname IS NULL THEN
        RAISE EXCEPTION 'outcome released identity check not found';
    END IF;
    EXECUTE format('ALTER TABLE outcome_events DROP CONSTRAINT %I', cname);
END $$;

ALTER TABLE outcome_events
    ADD CONSTRAINT outcome_events_released_identity CHECK (
        (kind <> 'released' AND release_node_id IS NULL)
        OR (
            kind = 'released'
            AND (
                release_node_id IS NOT NULL
                OR NULLIF(btrim(coalesce(payload->>'version', '')), '') IS NOT NULL
            )
        )
    );

CREATE OR REPLACE FUNCTION aeon_record_released_snapshot() RETURNS trigger
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
    clean_version := NULLIF(btrim(coalesce(version, '')), '');
    -- A journey snapshot always names its release. A manifest snapshot may not:
    -- tenant, project and version are the identity, and a blank version is not one.
    IF jsonb_typeof(NEW.snapshot->'tickets') IS DISTINCT FROM 'array'
       OR (release_id IS NULL AND clean_version IS NULL) THEN
        RETURN NEW;
    END IF;
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
        IF release_id IS NULL THEN
            outcome_key := 'auto:released:' || ticket_id::text || ':' || NEW.project_node_id::text || ':' || clean_version;
        ELSE
            outcome_key := 'auto:released:' || ticket_id::text || ':' || release_id::text || ':' || coalesce(clean_version, '');
        END IF;
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

DROP TRIGGER IF EXISTS outcome_events_released_manifest ON release_manifest_note_snapshots;
CREATE TRIGGER outcome_events_released_manifest
    AFTER INSERT ON release_manifest_note_snapshots
    FOR EACH ROW
    EXECUTE FUNCTION aeon_record_released_snapshot();
