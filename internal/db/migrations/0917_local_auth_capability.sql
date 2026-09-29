-- SPDX-License-Identifier: AGPL-3.0-only
-- Advisory report from the paired daemon. An omitted registration is stored as
-- unreported. This column never grants watch consent.
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_pairing_computers
 ADD COLUMN local_auth_capability text NOT NULL DEFAULT 'unreported'
 CHECK (local_auth_capability IN ('unreported','available','unsupported','unsigned','no_gui','policy'));
