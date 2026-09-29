-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-299: why the last account probe failed. auth_failed only when the
-- vendor's own status command confirmed a sign-out for that account; every
-- other failure (timeout, missing binary, unreadable output) is unavailable.
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_accounts ADD COLUMN last_probe_failure text NOT NULL DEFAULT ''
    CHECK (last_probe_failure IN ('', 'auth_failed', 'unavailable'));
UPDATE agent_accounts SET last_probe_failure = 'unavailable' WHERE last_probe_ok = false;
