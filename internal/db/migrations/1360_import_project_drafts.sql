-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1441: a conversation handed over from START waits here as a project
-- draft until a signed-in person creates the project from it. Expansion only:
-- a new table that no released writer touches.
--
-- The capability in the person's address is 256 random bits; only its SHA-256
-- is stored, so a database copy cannot open a draft. The payload is the
-- validated v1 document and is cleared when the project is created. Rows are
-- deleted at expires_at by the server's expiry sweep, independent of traffic:
-- 30 minutes for an unused draft, a day after creation (ids only) so a retry
-- by the same person can still find the created project.
SET LOCAL lock_timeout = '5s';
CREATE TABLE import_project_drafts (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 capability_digest bytea NOT NULL CHECK (length(capability_digest) = 32),
 payload jsonb,
 payload_bytes integer NOT NULL CHECK (payload_bytes BETWEEN 0 AND 524288),
 language text NOT NULL CHECK (language IN ('de', 'en')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL,
 consumed_at timestamptz,
 consumed_by uuid,
 project_id uuid,
 ticket_count integer,
 PRIMARY KEY (tenant_id, id)
);
CREATE UNIQUE INDEX import_project_drafts_capability ON import_project_drafts (tenant_id, capability_digest);
CREATE INDEX import_project_drafts_expiry ON import_project_drafts (tenant_id, expires_at);
ALTER TABLE import_project_drafts ENABLE ROW LEVEL SECURITY;
ALTER TABLE import_project_drafts FORCE ROW LEVEL SECURITY;
-- Drafts are never listed. Every read is a capability lookup inside the one
-- configured import tenant; the person's own tenant must match it.
CREATE POLICY import_project_drafts_tenant ON import_project_drafts
 USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
 WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
