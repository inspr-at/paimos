-- SPDX-License-Identifier: AGPL-3.0-only
-- Text retains original JSON spelling and evidence. Existing draft RLS and
-- immutability triggers protect these additive fields as part of the same row.
ALTER TABLE intake_drafts
    ADD COLUMN extensions text CHECK (extensions IS NULL OR json_typeof(extensions::json) = 'object'),
    ADD COLUMN document_bytes text;
