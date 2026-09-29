-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-125. A pace link counts releases. It does not publish their notes.
-- Existing rows stay off until the portal admin turns release history on.
SET LOCAL lock_timeout = '5s';

ALTER TABLE portal_pace
    ADD COLUMN release_history boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN portal_pace.release_history IS
    'When true, frozen public notes for this linked project are served. Default off, including for links that already exist.';
