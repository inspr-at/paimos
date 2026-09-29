-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-125. Release history is published only for the project and link
-- revision the admin still has on screen. Existing links start at 1.
SET LOCAL lock_timeout = '5s';

ALTER TABLE portal_pace
    ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision >= 1);

COMMENT ON COLUMN portal_pace.revision IS
    'Advances on every successful link write and every successful release-history write. A history write matches this value and the project together.';
