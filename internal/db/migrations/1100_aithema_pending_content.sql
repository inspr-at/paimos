-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-483: AIT-89 immutable content, event aliases and durable chunk staging.
-- The original byte constraint is relaxed only for snapshots/content; previous
-- binaries retain their ordinary write bound. Exact-byte policy exception is
-- recorded for coordinator review in migration-policy-exceptions.json.
SET LOCAL lock_timeout = '5s';

ALTER TABLE aithema_journal_records
    DROP CONSTRAINT aithema_journal_records_original_bytes_check,
    ADD CONSTRAINT aithema_journal_records_original_bytes_check CHECK (
        octet_length(original_bytes) <= 1048576
        OR contract = 'aithema.spec.snapshot'
        OR (contract = 'aithema.journal.record' AND kind = 'pending_op.content')
    );
ALTER TABLE aithema_journal_records ADD COLUMN content_sha256 text;
CREATE UNIQUE INDEX aithema_journal_content_address
    ON aithema_journal_records(tenant_id, sid, content_sha256)
    WHERE content_sha256 IS NOT NULL;

-- Every deduplicated submission retains its own bytes for event-ID conflicts,
-- while acknowledgements and dependency reads use the original content record.
CREATE TABLE aithema_journal_content_events (
    tenant_id uuid NOT NULL,
    sid uuid NOT NULL,
    client_event_id uuid NOT NULL,
    seq bigint NOT NULL,
    original_bytes bytea NOT NULL,
    PRIMARY KEY (tenant_id, sid, client_event_id),
    FOREIGN KEY (tenant_id, sid, seq) REFERENCES aithema_journal_records(tenant_id, sid, seq) ON DELETE CASCADE
);

CREATE TABLE aithema_journal_uploads (
    tenant_id uuid NOT NULL,
    sid uuid NOT NULL,
    worker_generation bigint NOT NULL CHECK (worker_generation BETWEEN 1 AND 9007199254740991),
    auth_epoch bigint NOT NULL CHECK (auth_epoch BETWEEN 1 AND 9007199254740991),
    action text NOT NULL CHECK (action IN ('records', 'snapshots')),
    wire_sha256 text NOT NULL CHECK (wire_sha256 ~ '^[0-9a-f]{64}$'),
    total bigint NOT NULL CHECK (total BETWEEN 1048577 AND 9007199254740991),
    next_offset bigint NOT NULL CHECK (next_offset BETWEEN 0 AND total),
    PRIMARY KEY (tenant_id, sid, worker_generation, auth_epoch, action, wire_sha256),
    FOREIGN KEY (tenant_id, sid) REFERENCES aithema_sessions(tenant_id, sid) ON DELETE CASCADE
);
CREATE TABLE aithema_journal_upload_chunks (
    tenant_id uuid NOT NULL,
    sid uuid NOT NULL,
    worker_generation bigint NOT NULL,
    auth_epoch bigint NOT NULL,
    action text NOT NULL,
    wire_sha256 text NOT NULL,
    chunk_offset bigint NOT NULL,
    bytes bytea NOT NULL CHECK (octet_length(bytes) BETWEEN 1 AND 262144),
    PRIMARY KEY (tenant_id, sid, worker_generation, auth_epoch, action, wire_sha256, chunk_offset),
    FOREIGN KEY (tenant_id, sid, worker_generation, auth_epoch, action, wire_sha256)
        REFERENCES aithema_journal_uploads(tenant_id, sid, worker_generation, auth_epoch, action, wire_sha256) ON DELETE CASCADE
);

ALTER TABLE aithema_journal_content_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE aithema_journal_content_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON aithema_journal_content_events
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
ALTER TABLE aithema_journal_uploads ENABLE ROW LEVEL SECURITY;
ALTER TABLE aithema_journal_uploads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON aithema_journal_uploads
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
ALTER TABLE aithema_journal_upload_chunks ENABLE ROW LEVEL SECURITY;
ALTER TABLE aithema_journal_upload_chunks FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON aithema_journal_upload_chunks
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
