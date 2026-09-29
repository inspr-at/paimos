-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-251: store comparison limit tokens, including an omitted Claude import.
-- Tokens only. Paths and instruction text stay out of this column.
SET LOCAL lock_timeout = '5s';

ALTER TABLE rules_comparisons
    ADD COLUMN gaps jsonb NOT NULL DEFAULT '[]'::jsonb
    CHECK (
        jsonb_typeof(gaps) = 'array'
        AND jsonb_array_length(gaps) <= 16
        AND octet_length(gaps::text) <= 2048
    );
