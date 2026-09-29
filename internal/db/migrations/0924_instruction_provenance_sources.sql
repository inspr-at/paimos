-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-219: server-recorded merged rules and rule-set identities on the
-- existing append-only provenance tables, plus the set list that was actually
-- served with a merge response. Rows still store a logical name, a hash and a
-- version. They do not store rule text, file contents or paths.
--
-- The provenance ordinal bound is the rules publication budget (2000 sets,
-- each counting as at least one rule) plus one merged identity plus local
-- instruction files (16 identities in one worker post, plus AGENTS.md,
-- CLAUDE.md and the prompt template, which a later partial report can add
-- without replacing skills). 19 + 1 + 2000 = 2020. Keep it equal to
-- harness.automaticProvenanceLimit.
SET LOCAL lock_timeout = '5s';

ALTER TABLE harness_instruction_provenance_items DROP CONSTRAINT harness_instruction_provenance_items_ordinal_check;
ALTER TABLE harness_instruction_provenance_items ADD CONSTRAINT harness_instruction_provenance_items_ordinal_check
    CHECK (ordinal >= 0 AND ordinal < 2020);

ALTER TABLE harness_instruction_provenance_items DROP CONSTRAINT harness_instruction_provenance_items_kind_check;
ALTER TABLE harness_instruction_provenance_items ADD CONSTRAINT harness_instruction_provenance_items_kind_check
    CHECK (kind IN ('agents', 'claude', 'skill', 'prompt_template', 'rules_merged', 'rules_set'));

ALTER TABLE harness_instruction_provenance_items DROP CONSTRAINT harness_instruction_provenance_items_check;
ALTER TABLE harness_instruction_provenance_items ADD CONSTRAINT harness_instruction_provenance_items_check
    CHECK (
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
            OR (kind = 'rules_merged' AND logical_name = 'merged-rules' AND version IS NOT NULL AND byte_size IS NOT NULL)
            OR (kind = 'rules_set' AND logical_name ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' AND version IS NOT NULL AND byte_size IS NULL)
        )
    );

CREATE INDEX harness_instruction_provenance_items_version
    ON harness_instruction_provenance_items (tenant_id, version)
    WHERE version IS NOT NULL;

-- Absent agent or task is stored as 00000000-0000-4000-8000-000000000000, the
-- budget-check placeholder, so the identity is a plain unique key. sets is a
-- JSON array of {set_id, version, sha256}. It has no rule text.
CREATE TABLE rule_served_manifests (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL,
    person_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    task_id uuid NOT NULL,
    role text NOT NULL CHECK (role IN ('coordinator', 'builder', 'reviewer', 'operator')),
    harness text NOT NULL CHECK (harness IN ('claude-code', 'codex', 'grok', 'pi', 'cursor')),
    version text NOT NULL CHECK (char_length(version) BETWEEN 1 AND 80),
    body_sha256 text NOT NULL CHECK (body_sha256 ~ '^[0-9a-f]{64}$'),
    byte_size integer NOT NULL CHECK (byte_size >= 1 AND byte_size <= 12000),
    sets_digest bytea NOT NULL CHECK (octet_length(sets_digest) = 32),
    sets jsonb NOT NULL CHECK (jsonb_typeof(sets) = 'array' AND jsonb_array_length(sets) BETWEEN 1 AND 2000),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    CONSTRAINT rule_served_manifests_identity UNIQUE (
        tenant_id, project_id, person_id, agent_id, task_id, role, harness, version, body_sha256, byte_size, sets_digest)
);
ALTER TABLE rule_served_manifests ENABLE ROW LEVEL SECURITY;
ALTER TABLE rule_served_manifests FORCE ROW LEVEL SECURITY;
CREATE POLICY rule_served_manifests_tenant ON rule_served_manifests
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE TRIGGER rule_served_manifests_immutable
    BEFORE UPDATE OR DELETE ON rule_served_manifests
    FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();
