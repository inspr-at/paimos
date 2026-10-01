-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-501: widen only the harness family constraint. Every existing row/value
-- remains valid and the previous binary can still write every existing family.
SET LOCAL lock_timeout = '5s';
ALTER TABLE harness_sessions DROP CONSTRAINT harness_sessions_harness_check;
ALTER TABLE harness_sessions ADD CONSTRAINT harness_sessions_harness_check
    CHECK (harness IN ('codex','claude','pi','cursor','grok','media','terminal')) NOT VALID;
ALTER TABLE harness_sessions VALIDATE CONSTRAINT harness_sessions_harness_check;
ALTER TABLE harness_sessions
    ADD COLUMN generator text,
    ADD COLUMN command text;
ALTER TABLE harness_sessions ADD CONSTRAINT harness_run_label_check CHECK (
    (harness = 'media' AND generator IS NOT NULL AND generator ~ '^[A-Za-z0-9][A-Za-z0-9._:/+-]{0,119}$' AND command IS NULL)
    OR (harness = 'terminal' AND command IS NOT NULL AND command ~ '^[A-Za-z0-9][A-Za-z0-9._:/+ -]{0,119}$' AND command = btrim(command) AND generator IS NULL)
    OR (harness NOT IN ('media','terminal') AND generator IS NULL AND command IS NULL)
) NOT VALID;
ALTER TABLE harness_sessions VALIDATE CONSTRAINT harness_run_label_check;
