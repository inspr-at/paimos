-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-975: exact participant read evidence; no legacy watermark is imported.
CREATE TABLE chat_seen_chunks (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    conversation_id uuid NOT NULL,
    person_id uuid NOT NULL,
    chunk_index bigint NOT NULL,
    bitmap bytea NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (tenant_id,conversation_id,person_id,chunk_index)
);
ALTER TABLE chat_seen_chunks ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_seen_chunks FORCE ROW LEVEL SECURITY;
CREATE POLICY chat_seen_chunks_tenant ON chat_seen_chunks
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY chat_seen_chunks_person ON chat_seen_chunks AS RESTRICTIVE
    USING (person_id=ANY((SELECT aeon_current_principals())::uuid[]));

CREATE TABLE chat_read_state (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    conversation_id uuid NOT NULL,
    person_id uuid NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (tenant_id,conversation_id,person_id)
);
ALTER TABLE chat_read_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_read_state FORCE ROW LEVEL SECURITY;
CREATE POLICY chat_read_state_tenant ON chat_read_state
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY chat_read_state_person ON chat_read_state AS RESTRICTIVE
    USING (person_id=ANY((SELECT aeon_current_principals())::uuid[]));
