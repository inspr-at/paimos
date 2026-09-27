-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-213/214: public, tenant-scoped session setup and work context.
ALTER TABLE harness_sessions
    ADD COLUMN model text CHECK (model IS NULL OR (char_length(model) BETWEEN 1 AND 120 AND model = btrim(model) AND model !~ '[[:cntrl:]]')),
    ADD COLUMN reasoning_effort text CHECK (reasoning_effort IS NULL OR (char_length(reasoning_effort) BETWEEN 1 AND 40 AND reasoning_effort = btrim(reasoning_effort) AND reasoning_effort !~ '[[:cntrl:]]')),
    ADD COLUMN account_label text CHECK (account_label IS NULL OR (char_length(account_label) BETWEEN 1 AND 60 AND account_label = btrim(account_label) AND account_label !~ '[[:cntrl:]]')),
    ADD COLUMN harness_version text CHECK (harness_version IS NULL OR (char_length(harness_version) BETWEEN 1 AND 80 AND harness_version = btrim(harness_version) AND harness_version !~ '[[:cntrl:]]')),
    ADD COLUMN brief text CHECK (brief IS NULL OR (char_length(brief) BETWEEN 1 AND 240 AND brief = btrim(brief) AND brief !~ '[[:cntrl:]]')),
    ADD COLUMN worktree text CHECK (worktree IS NULL OR (char_length(worktree) BETWEEN 1 AND 512 AND worktree = btrim(worktree) AND worktree !~ '[[:cntrl:]]')),
    ADD COLUMN branch text CHECK (branch IS NULL OR (char_length(branch) BETWEEN 1 AND 200 AND branch = btrim(branch) AND branch !~ '[[:cntrl:]]')),
    ADD COLUMN commits jsonb NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(commits) = 'array' AND jsonb_array_length(commits) <= 20),
    ADD COLUMN registration_metadata_digest bytea
        CHECK (registration_metadata_digest IS NULL OR octet_length(registration_metadata_digest) = 32);
