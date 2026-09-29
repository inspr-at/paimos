-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-280: delivered or loudly not delivered. Every new queued receipt to an
-- agent carries its deadline; the server sweeper fails it past that deadline.
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
    ADD COLUMN inbox_seen_via text CHECK (inbox_seen_via IN ('hook', 'drain', 'long_poll', 'stream', 'ack'));

ALTER TABLE inbox_message_deliveries DROP CONSTRAINT inbox_message_deliveries_reason_check;
ALTER TABLE inbox_message_deliveries ADD CONSTRAINT inbox_message_deliveries_reason_check
    CHECK (reason IN ('', 'target_missing', 'action_request', 'unsupported', 'unavailable',
        'target_invalid', 'payload_invalid', 'transport_error', 'http_error',
        'deadline', 'attempts', 'session_ended', 'no_listener'));

-- The sweeper reads only open deadlines; every row here is new, so the build is cheap.
CREATE INDEX inbox_receipts_open_deadline ON inbox_receipts(tenant_id, deliver_by)
    WHERE state = 'queued' AND deliver_by IS NOT NULL;
