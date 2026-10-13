-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1271 P3a expansion. Content-free native receipt/final projections only.
-- Input/final bodies remain in authorized existing inbox/chat storage, and
-- explicitly approved final retries in the private, bounded local outbox.
SET LOCAL lock_timeout = '5s';

CREATE TABLE native_session_receipts (
 tenant_id uuid NOT NULL,
 operation_id uuid NOT NULL,
 request_id uuid NOT NULL,
 project_id uuid NOT NULL,
 owned_session_id uuid NOT NULL,
 owner_person_id uuid NOT NULL,
 actor_principal_id uuid NOT NULL,
 payload_sha256 text NOT NULL,
 expires_at timestamptz NOT NULL,
 receipt jsonb NOT NULL,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 attempts integer NOT NULL DEFAULT 0,
 PRIMARY KEY (tenant_id, operation_id),
 UNIQUE (tenant_id, request_id)
);
CREATE INDEX native_session_receipts_page ON native_session_receipts
 (tenant_id, owned_session_id, operation_id);
CREATE UNIQUE INDEX native_session_receipts_input ON native_session_receipts
 (tenant_id, owned_session_id, (receipt->>'input_id'))
 WHERE receipt->>'operation' = 'input';
ALTER TABLE native_session_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE native_session_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY native_session_receipts_tenant ON native_session_receipts
 USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
 WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY native_session_receipts_project ON native_session_receipts AS RESTRICTIVE
 USING (EXISTS (SELECT 1 FROM nodes n WHERE n.tenant_id=native_session_receipts.tenant_id AND n.id=native_session_receipts.project_id))
 WITH CHECK (EXISTS (SELECT 1 FROM nodes n WHERE n.tenant_id=native_session_receipts.tenant_id AND n.id=native_session_receipts.project_id));

CREATE TABLE native_session_finals (
 tenant_id uuid NOT NULL,
 final_id uuid NOT NULL,
 project_id uuid NOT NULL,
 owned_session_id uuid NOT NULL,
 owner_person_id uuid NOT NULL,
 sender_principal_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 drain_grant_id uuid NOT NULL,
 binding jsonb NOT NULL,
 input_id uuid NOT NULL,
 turn_ref text NOT NULL,
 publication_sha256 text NOT NULL,
 text_bytes integer NOT NULL,
 message_id uuid,
 state text NOT NULL,
 reason text,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 PRIMARY KEY (tenant_id, final_id)
);
CREATE INDEX native_session_finals_grant ON native_session_finals
 (tenant_id, drain_grant_id, final_id);
ALTER TABLE native_session_finals ENABLE ROW LEVEL SECURITY;
ALTER TABLE native_session_finals FORCE ROW LEVEL SECURITY;
CREATE POLICY native_session_finals_tenant ON native_session_finals
 USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
 WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY native_session_finals_project ON native_session_finals AS RESTRICTIVE
 USING (EXISTS (SELECT 1 FROM nodes n WHERE n.tenant_id=native_session_finals.tenant_id AND n.id=native_session_finals.project_id))
 WITH CHECK (EXISTS (SELECT 1 FROM nodes n WHERE n.tenant_id=native_session_finals.tenant_id AND n.id=native_session_finals.project_id));
