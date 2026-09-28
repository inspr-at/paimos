-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
CREATE INDEX CONCURRENTLY inbox_session_pending ON inbox_messages(tenant_id,recipient_session_id,sent_event_id) WHERE recipient_session_id IS NOT NULL AND acked_at IS NULL;
