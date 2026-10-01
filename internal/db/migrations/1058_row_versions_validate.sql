-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-449: the runner commits each file separately. Validate after 1052 releases
-- ACCESS EXCLUSIVE; SHARE UPDATE EXCLUSIVE validation permits concurrent writes.
SET LOCAL lock_timeout = '5s';
ALTER TABLE harness_sessions VALIDATE CONSTRAINT harness_sessions_row_version_check;
ALTER TABLE agent_runs VALIDATE CONSTRAINT agent_runs_row_version_check;
