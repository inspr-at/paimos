-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-262. Validate the progress check added NOT VALID in 0907.

SET LOCAL lock_timeout = '5s';

ALTER TABLE harness_sessions VALIDATE CONSTRAINT harness_sessions_progress_pct;
