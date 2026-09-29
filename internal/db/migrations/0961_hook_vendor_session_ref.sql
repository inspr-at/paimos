-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-311. Digested harness-native session id for hook binding.
-- The raw reference is never stored. Lookup matches ref_digest or this column.

SET LOCAL lock_timeout = '5s';

ALTER TABLE harness_sessions ADD COLUMN vendor_ref_digest bytea;

-- harness_sessions is small. A plain CREATE INDEX under lock_timeout is acceptable
-- here; CREATE INDEX CONCURRENTLY cannot run inside this migration transaction.
-- One active vendor reference per caller. Another principal's row must not block it.
CREATE UNIQUE INDEX harness_one_active_vendor_ref
    ON harness_sessions (tenant_id, agent_principal_id, vendor_ref_digest)
    WHERE vendor_ref_digest IS NOT NULL AND stopped_at IS NULL AND archived_at IS NULL;
