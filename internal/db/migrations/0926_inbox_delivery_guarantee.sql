-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-280: delivered or loudly not delivered. Every new queued receipt to an
-- agent carries its deadline; the server sweeper fails it past that deadline.
-- Metadata-only DDL: new constraints are NOT VALID (0927 validates them with
-- SHARE UPDATE EXCLUSIVE) and the sweeper index is built concurrently (0928).
SET LOCAL lock_timeout = '5s';

-- Tenant choice of deadlines and the adapter attempt cap. No row = defaults.
CREATE TABLE inbox_delivery_settings (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
    session_deadline_seconds integer NOT NULL DEFAULT 300 CHECK (session_deadline_seconds BETWEEN 60 AND 86400),
    unbound_deadline_seconds integer NOT NULL DEFAULT 1800 CHECK (unbound_deadline_seconds BETWEEN 60 AND 604800),
    max_attempts integer NOT NULL DEFAULT 8 CHECK (max_attempts BETWEEN 1 AND 50),
    updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE inbox_delivery_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE inbox_delivery_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON inbox_delivery_settings
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

-- Null for receipts written before this migration: history is never failed
-- retroactively, so no sender gets a flood of notices for old messages.
ALTER TABLE inbox_receipts ADD COLUMN deliver_by timestamptz;
-- First time the message was handed to its recipient (pull, drain, stream, claim).
ALTER TABLE inbox_messages ADD COLUMN fetched_at timestamptz;
-- Last time this generation pulled its inbox, and how. Additive session metadata.
ALTER TABLE harness_sessions
    ADD COLUMN inbox_seen_at timestamptz,
    ADD COLUMN inbox_seen_via text;
ALTER TABLE harness_sessions ADD CONSTRAINT harness_sessions_inbox_seen_via
    CHECK (inbox_seen_via IN ('hook', 'drain', 'long_poll', 'stream', 'ack')) NOT VALID;

ALTER TABLE inbox_message_deliveries DROP CONSTRAINT inbox_message_deliveries_reason_check;
ALTER TABLE inbox_message_deliveries ADD CONSTRAINT inbox_message_deliveries_reason_check
    CHECK (reason IN ('', 'target_missing', 'action_request', 'unsupported', 'unavailable',
        'target_invalid', 'payload_invalid', 'transport_error', 'http_error',
        'deadline', 'attempts', 'session_ended', 'no_listener')) NOT VALID;

-- The one system write path for delivery failures. A caller that sees only
-- some projects cannot read a node-less System event back (events SELECT
-- policy), so events.Append's INSERT ... RETURNING would roll the caller back.
-- Workspace-wide visibility is lifted for this one insert only and restored
-- before returning (an error aborts the transaction, which discards it too);
-- it is limited to the current tenant, the System actor, these inbox types
-- and a JSON object payload carrying each type's keys. search_path is pinned
-- (pg_temp last) and every reference is schema-qualified, so a caller's
-- temporary objects can never run while visibility is lifted.
CREATE FUNCTION public.aeon_inbox_system_event(target_tenant uuid, event_type text, event_after jsonb) RETURNS bigint
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    actor uuid;
    event_id bigint;
    required text[];
    visible text := coalesce(pg_catalog.current_setting('aeon.visible_projects', true), '');
BEGIN
    IF target_tenant IS DISTINCT FROM nullif(pg_catalog.current_setting('aeon.tenant_id', true), '')::uuid THEN
        RAISE EXCEPTION 'system inbox event outside the current tenant' USING ERRCODE = '42501';
    END IF;
    required := CASE event_type
        WHEN 'inbox.delivery_failed' THEN ARRAY['message_id', 'reason', 'sender_principal_id', 'recipient_principal_id']
        WHEN 'inbox.receipt_failed' THEN ARRAY['message_id', 'state', 'failure_reason']
        WHEN 'inbox.receipt_handed_off' THEN ARRAY['message_id', 'state']
        WHEN 'inbox.sent' THEN ARRAY['id', 'sender_principal_id', 'recipient_principal_id']
        WHEN 'inbox.wake_queued' THEN ARRAY['message_id', 'target_id']
    END;
    IF required IS NULL THEN
        RAISE EXCEPTION 'system inbox event type % is not allowed', event_type USING ERRCODE = '42501';
    END IF;
    IF event_after IS NULL OR pg_catalog.jsonb_typeof(event_after) <> 'object' OR NOT (event_after OPERATOR(pg_catalog.?&) required) THEN
        RAISE EXCEPTION 'system inbox event % needs an object with %', event_type, required USING ERRCODE = '22023';
    END IF;
    actor := public.aeon_authz_system_actor(target_tenant);
    PERFORM pg_catalog.set_config('aeon.visible_projects', '*', true);
    INSERT INTO public.events(tenant_id, actor_principal_id, type, after, at)
    VALUES (target_tenant, actor, event_type, event_after, pg_catalog.clock_timestamp())
    RETURNING id INTO event_id;
    PERFORM pg_catalog.set_config('aeon.visible_projects', visible, true);
    RETURN event_id;
END;
$$;
