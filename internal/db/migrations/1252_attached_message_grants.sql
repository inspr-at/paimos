-- SPDX-License-Identifier: AGPL-3.0-only
-- Authority only; message bodies remain in process memory (AEON-392).
SET LOCAL lock_timeout = '5s';
CREATE TABLE attached_message_grants (
 tenant_id uuid NOT NULL REFERENCES tenants(id), id uuid NOT NULL DEFAULT gen_random_uuid(),
 attach_request_id uuid NOT NULL, session_id uuid NOT NULL, computer_id uuid NOT NULL,
 owner_id uuid NOT NULL, project_id uuid NOT NULL, message_generation uuid NOT NULL DEFAULT gen_random_uuid(),
 binding jsonb NOT NULL, consent_digest text NOT NULL CHECK(consent_digest ~ '^[a-f0-9]{64}$'),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','approved','active','revoked')),
 expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '10 minutes',
 local_auth_nonce text CHECK(local_auth_nonce IS NULL OR local_auth_nonce ~ '^[a-f0-9]{64}$'),
 hook_observed_at timestamptz, created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,message_generation),
 FOREIGN KEY(tenant_id,attach_request_id) REFERENCES harness_attach_requests(tenant_id,id),
 FOREIGN KEY(tenant_id,session_id) REFERENCES harness_sessions(tenant_id,id),
 FOREIGN KEY(tenant_id,computer_id) REFERENCES agent_pairing_computers(tenant_id,id),
 FOREIGN KEY(tenant_id,owner_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id)
);
CREATE UNIQUE INDEX attached_message_grant_live ON attached_message_grants(tenant_id,attach_request_id) WHERE state<>'revoked';
ALTER TABLE attached_message_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE attached_message_grants FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON attached_message_grants
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
-- Counters live on existing authority records, never a second inbox/queue.
ALTER TABLE harness_attach_requests ADD COLUMN message_tokens double precision NOT NULL DEFAULT 3,
 ADD COLUMN message_tokens_at timestamptz NOT NULL DEFAULT 'epoch'::timestamptz,
 ADD COLUMN message_notice_tokens double precision NOT NULL DEFAULT 3,
 ADD COLUMN message_notice_tokens_at timestamptz NOT NULL DEFAULT 'epoch'::timestamptz,
 ADD COLUMN message_notice_at timestamptz,
 ADD COLUMN message_notice_count integer NOT NULL DEFAULT 0 CHECK(message_notice_count BETWEEN 0 AND 255);
ALTER TABLE agent_pairing_computers ADD COLUMN message_window_at timestamptz NOT NULL DEFAULT 'epoch'::timestamptz,
 ADD COLUMN message_window_count integer NOT NULL DEFAULT 0,
 ADD COLUMN message_notice_window_at timestamptz NOT NULL DEFAULT 'epoch'::timestamptz,
 ADD COLUMN message_notice_window_count integer NOT NULL DEFAULT 0;
