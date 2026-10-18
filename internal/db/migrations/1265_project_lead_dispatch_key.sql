-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

-- The agent key row that claimed, or last provably dispatched for, the bound
-- lead generation. Fair-turn scheduling qualifies exactly this credential with
-- its live constraints; another key of the same principal is not the lead's
-- dispatch authority. NULL preserves existing writers and means the current
-- generation has not yet proven a dispatch credential, so it holds no turn.
ALTER TABLE project_leads ADD COLUMN dispatch_key_id uuid;
