-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1474: existing writers leave Desk notices off; existing tenant RLS applies.
ALTER TABLE phone_push_subscriptions ADD COLUMN desk_enabled boolean DEFAULT false;
-- Keep the existing claim state constraint compatible with older writers.
-- A summarised item is sent as part of a summary, with explicit evidence here.
ALTER TABLE desk_notification_claims ADD COLUMN summarised_at timestamptz;
CREATE INDEX phone_push_desk_recipients ON phone_push_subscriptions(tenant_id,person_id,id)
 WHERE desk_enabled AND revoked_at IS NULL;
