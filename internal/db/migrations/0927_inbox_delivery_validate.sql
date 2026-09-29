-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-280. Validate the checks added NOT VALID in 0926. SHARE UPDATE
-- EXCLUSIVE validation permits normal reads and writes.
SET LOCAL lock_timeout = '5s';

ALTER TABLE harness_sessions VALIDATE CONSTRAINT harness_sessions_inbox_seen_via;
ALTER TABLE inbox_message_deliveries VALIDATE CONSTRAINT inbox_message_deliveries_reason_check;
