-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-286. release_manifest_note_snapshots arrives in 0939, after the
-- outcome function in 0933, so this trigger is created once that table exists.
-- A historical manifest capture records the same released outcome as publication.

SET LOCAL lock_timeout = '5s';

CREATE TRIGGER outcome_events_released_manifest
    AFTER INSERT ON release_manifest_note_snapshots
    FOR EACH ROW
    EXECUTE FUNCTION aeon_record_released_snapshot();
