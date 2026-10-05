-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-503: forward-only timing. Preserve legacy active_ms and all old evidence.
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS waiting_ms bigint;
