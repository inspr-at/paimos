-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-483: build the immutable content address without blocking writers.
CREATE UNIQUE INDEX CONCURRENTLY aithema_journal_content_address
    ON aithema_journal_records(tenant_id, sid, content_sha256)
    WHERE content_sha256 IS NOT NULL;
