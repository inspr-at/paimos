-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-219: append-only instruction provenance for one harness session.
-- Rows store an allowlisted logical name, a hash kind, a sha256 digest when
-- the kind is content, and an optional version identifier. A prompt template
-- with no supplied template digest stores hash_kind absent and a null digest.
-- They do not store file contents, secrets, local paths, or a hash of a version.
CREATE FUNCTION aeon_instruction_provenance_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'instruction provenance is append-only';
END;
$$;

CREATE TABLE harness_instruction_provenance (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    session_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    set_digest bytea NOT NULL CHECK (octet_length(set_digest) = 32),
    recorded_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, session_id, revision),
    FOREIGN KEY (tenant_id, session_id) REFERENCES harness_sessions(tenant_id, id),
    FOREIGN KEY (tenant_id, recorded_by) REFERENCES principals(tenant_id, id)
);
CREATE INDEX harness_instruction_provenance_session
    ON harness_instruction_provenance (tenant_id, session_id, revision DESC);
ALTER TABLE harness_instruction_provenance ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_instruction_provenance FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_instruction_provenance_tenant ON harness_instruction_provenance
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY harness_instruction_provenance_project_visibility ON harness_instruction_provenance AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR EXISTS (
        SELECT 1 FROM harness_sessions s
        WHERE s.tenant_id = harness_instruction_provenance.tenant_id
          AND s.id = harness_instruction_provenance.session_id))
    WITH CHECK ((SELECT aeon_visible_all()) OR EXISTS (
        SELECT 1 FROM harness_sessions s
        WHERE s.tenant_id = harness_instruction_provenance.tenant_id
          AND s.id = harness_instruction_provenance.session_id));
CREATE TRIGGER harness_instruction_provenance_immutable
    BEFORE UPDATE OR DELETE ON harness_instruction_provenance
    FOR EACH ROW EXECUTE FUNCTION aeon_instruction_provenance_append_only();

CREATE TABLE harness_instruction_provenance_items (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    provenance_id uuid NOT NULL,
    ordinal smallint NOT NULL CHECK (ordinal >= 0 AND ordinal < 16),
    kind text NOT NULL CHECK (kind IN ('agents', 'claude', 'skill', 'prompt_template')),
    hash_kind text NOT NULL CHECK (hash_kind IN ('content', 'absent')),
    logical_name text NOT NULL CHECK (
        char_length(logical_name) BETWEEN 1 AND 80
        AND logical_name = btrim(logical_name)
        AND logical_name !~ '[[:cntrl:]]'
        AND strpos(logical_name, chr(92)) = 0
        AND logical_name NOT LIKE '%..%'
        AND logical_name NOT LIKE '~%'
        AND (
            (kind = 'agents' AND logical_name = 'AGENTS.md' AND byte_size IS NOT NULL)
            OR (kind = 'claude' AND logical_name = 'CLAUDE.md' AND byte_size IS NOT NULL)
            OR (kind = 'skill' AND logical_name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}/SKILL\.md$' AND byte_size IS NOT NULL)
            OR (kind = 'prompt_template' AND logical_name = 'prompt-template' AND version IS NOT NULL AND byte_size IS NULL)
        )
    ),
    content_sha256 text CHECK (
        (hash_kind = 'content' AND content_sha256 ~ '^[0-9a-f]{64}$')
        OR (hash_kind = 'absent' AND content_sha256 IS NULL AND kind = 'prompt_template')
    ),
    version text CHECK (
        version IS NULL OR (
            char_length(version) BETWEEN 1 AND 80
            AND version = btrim(version)
            AND version !~ '[[:cntrl:][:space:]]'
            AND strpos(version, chr(92)) = 0
            AND strpos(version, '/') = 0
            AND strpos(version, ':') = 0
            AND version NOT LIKE '%..%'
        )
    ),
    byte_size bigint CHECK (byte_size IS NULL OR (byte_size >= 0 AND byte_size <= 1048576)),
    PRIMARY KEY (tenant_id, provenance_id, ordinal),
    UNIQUE (tenant_id, provenance_id, logical_name),
    FOREIGN KEY (tenant_id, provenance_id) REFERENCES harness_instruction_provenance(tenant_id, id)
);
ALTER TABLE harness_instruction_provenance_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_instruction_provenance_items FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_instruction_provenance_items_tenant ON harness_instruction_provenance_items
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY harness_instruction_provenance_items_project_visibility ON harness_instruction_provenance_items AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR EXISTS (
        SELECT 1 FROM harness_instruction_provenance p
        WHERE p.tenant_id = harness_instruction_provenance_items.tenant_id
          AND p.id = harness_instruction_provenance_items.provenance_id))
    WITH CHECK ((SELECT aeon_visible_all()) OR EXISTS (
        SELECT 1 FROM harness_instruction_provenance p
        WHERE p.tenant_id = harness_instruction_provenance_items.tenant_id
          AND p.id = harness_instruction_provenance_items.provenance_id));
CREATE TRIGGER harness_instruction_provenance_items_immutable
    BEFORE UPDATE OR DELETE ON harness_instruction_provenance_items
    FOR EACH ROW EXECUTE FUNCTION aeon_instruction_provenance_append_only();
