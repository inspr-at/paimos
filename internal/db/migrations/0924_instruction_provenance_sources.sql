-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-219: server-recorded merged rules and rule-set identities on the
-- existing append-only provenance tables. Rows still store a logical name,
-- a hash and a version. They do not store rule text, file contents or paths.
SET LOCAL lock_timeout = '5s';

ALTER TABLE harness_instruction_provenance_items DROP CONSTRAINT harness_instruction_provenance_items_ordinal_check;
ALTER TABLE harness_instruction_provenance_items ADD CONSTRAINT harness_instruction_provenance_items_ordinal_check
    CHECK (ordinal >= 0 AND ordinal < 64);

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
