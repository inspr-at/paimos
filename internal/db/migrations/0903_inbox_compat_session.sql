-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
CREATE INDEX CONCURRENTLY inbox_compat_session ON inbox_compat_messages(tenant_id,project_id,recipient_session_id,sent_event_id) WHERE recipient_session_id IS NOT NULL;
