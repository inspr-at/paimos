-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-483: validate after 1100 releases its metadata-only exclusive lock.
SET LOCAL lock_timeout = '5s';
ALTER TABLE aithema_journal_records
    VALIDATE CONSTRAINT aithema_journal_records_content_bytes_check;
