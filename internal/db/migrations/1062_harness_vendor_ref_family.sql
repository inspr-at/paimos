-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-498: a vendor session reference belongs to an agent and harness family.
-- Preserve active duplicate protection while allowing different harnesses to
-- use the same native value. No generation, digest, lease or RLS policy changes.
SET LOCAL lock_timeout = '5s';

-- Install the replacement before removing the stricter index, in one transaction.
CREATE UNIQUE INDEX harness_one_active_vendor_ref_per_harness
    ON harness_sessions (tenant_id, agent_principal_id, harness, vendor_ref_digest)
    WHERE vendor_ref_digest IS NOT NULL AND stopped_at IS NULL AND archived_at IS NULL;

DROP INDEX harness_one_active_vendor_ref;
