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
-- it is limited to the current tenant, the System actor and these inbox types.
CREATE FUNCTION aeon_inbox_system_event(target_tenant uuid, event_type text, event_after jsonb) RETURNS bigint
LANGUAGE plpgsql
AS $$
DECLARE
    actor uuid;
    event_id bigint;
    visible text := coalesce(current_setting('aeon.visible_projects', true), '');
BEGIN
    IF target_tenant IS DISTINCT FROM NULLIF(current_setting('aeon.tenant_id', true), '')::uuid THEN
        RAISE EXCEPTION 'system inbox event outside the current tenant' USING ERRCODE = '42501';
    END IF;
    IF event_type NOT IN ('inbox.delivery_failed', 'inbox.receipt_failed', 'inbox.receipt_handed_off', 'inbox.sent', 'inbox.wake_queued') THEN
        RAISE EXCEPTION 'system inbox event type % is not allowed', event_type USING ERRCODE = '42501';
    END IF;
    actor := aeon_authz_system_actor(target_tenant);
    PERFORM set_config('aeon.visible_projects', '*', true);
    INSERT INTO events(tenant_id, actor_principal_id, type, after, at)
    VALUES (target_tenant, actor, event_type, event_after, clock_timestamp())
    RETURNING id INTO event_id;
    PERFORM set_config('aeon.visible_projects', visible, true);
    RETURN event_id;
END;
$$;
