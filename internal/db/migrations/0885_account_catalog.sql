-- SPDX-License-Identifier: AGPL-3.0-only
-- Extend the existing opaque enrollment; credentials and local homes stay local.
-- NULL retains the pre-existing tenant-harness policy. An explicit empty list
-- denies all profiles. Profile IDs are validated against tenant/harness on write.
ALTER TABLE agent_accounts
    ADD COLUMN plan text NOT NULL DEFAULT '' CHECK (length(plan) <= 128),
    ADD COLUMN host_label text NOT NULL DEFAULT '' CHECK (length(host_label) <= 128),
    ADD COLUMN allowed_model_profile_ids uuid[]
        CHECK (cardinality(allowed_model_profile_ids) <= 256);

-- Managed sessions report the exact account label and registry model, whose
-- source contracts already allow 128 characters. Never truncate audit metadata.
ALTER TABLE harness_sessions
    DROP CONSTRAINT harness_sessions_model_check,
    DROP CONSTRAINT harness_sessions_account_label_check,
    ADD CONSTRAINT harness_sessions_model_check CHECK (model IS NULL OR
        (char_length(model) BETWEEN 1 AND 128 AND model = btrim(model) AND model !~ '[[:cntrl:]]')),
    ADD CONSTRAINT harness_sessions_account_label_check CHECK (account_label IS NULL OR
        (char_length(account_label) BETWEEN 1 AND 128 AND account_label = btrim(account_label) AND account_label !~ '[[:cntrl:]]'));
