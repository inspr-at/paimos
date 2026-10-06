-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

-- One turn per successful new Queue route, across generations. NULL preserves
-- existing writers and means this intent has not taken a scheduling turn yet.
ALTER TABLE project_leads ADD COLUMN last_turn_at timestamptz;
