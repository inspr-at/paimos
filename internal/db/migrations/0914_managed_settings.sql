-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
ALTER TABLE harness_controls DROP CONSTRAINT harness_controls_kind_check;
ALTER TABLE harness_controls ADD CONSTRAINT harness_controls_kind_check
    CHECK (kind IN ('interrupt','stop','force_stop','steer','rename','model','effort'));
ALTER TABLE harness_controls ADD COLUMN value text;
ALTER TABLE harness_controls ADD CONSTRAINT harness_settings_value CHECK (
    CASE WHEN kind IN ('rename','model','effort') THEN
        value IS NOT NULL AND char_length(value) BETWEEN 1 AND 128
        AND value = btrim(value) AND value !~ '[[:cntrl:]]'
        AND expected_ownership IS NOT NULL AND request_digest IS NOT NULL AND expires_at IS NOT NULL
    ELSE value IS NULL END
);
