-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-345. Session thread walks filter sender_session_id.
-- recipient_session_id already has inbox_compat_session; replies are in 0993.
-- The runner bounds lock waits and rebuilds invalid indexes on retry.

CREATE INDEX CONCURRENTLY IF NOT EXISTS inbox_compat_sender_session ON inbox_compat_messages (tenant_id, project_id, sender_session_id, sent_event_id)
    WHERE sender_session_id IS NOT NULL;
