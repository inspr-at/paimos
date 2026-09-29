-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-345. Session thread walks filter sender_session_id and join reply_to_id.
-- recipient_session_id already has inbox_compat_session. inbox_compat_messages is
-- still small; a plain CREATE INDEX under lock_timeout is acceptable here.
-- CREATE INDEX CONCURRENTLY cannot run inside this migration transaction.

SET LOCAL lock_timeout = '5s';

CREATE INDEX inbox_compat_sender_session ON inbox_compat_messages (tenant_id, project_id, sender_session_id, sent_event_id)
    WHERE sender_session_id IS NOT NULL;

CREATE INDEX inbox_compat_reply ON inbox_compat_messages (tenant_id, project_id, reply_to_id)
    WHERE reply_to_id IS NOT NULL;
