-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-336: a background job can record why it wrote an event. The activity
-- timeline reads this; the public event list does not grow a field.
SET LOCAL lock_timeout = '5s';

ALTER TABLE events ADD COLUMN metadata jsonb;

ALTER TABLE events ADD CONSTRAINT events_metadata_object
    CHECK (metadata IS NULL OR jsonb_typeof(metadata) = 'object');
