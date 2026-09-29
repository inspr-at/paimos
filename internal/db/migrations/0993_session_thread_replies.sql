-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-345. Session thread walks follow reply_to_id without blocking writers.
CREATE INDEX CONCURRENTLY IF NOT EXISTS inbox_compat_reply ON inbox_compat_messages (tenant_id, project_id, reply_to_id)
    WHERE reply_to_id IS NOT NULL;
