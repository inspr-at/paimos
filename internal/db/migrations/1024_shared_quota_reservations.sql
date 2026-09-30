-- SPDX-License-Identifier: AGPL-3.0-only
-- A run owns its local door, but reserves the shared login's allowance ledger.
-- Preserve the tenant and pairing boundaries when using a sibling's window.
CREATE OR REPLACE FUNCTION aeon_guard_account_reservation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM agent_runs r
        JOIN agent_accounts door ON door.tenant_id=r.tenant_id AND door.id=r.account_id
        JOIN account_allowance_windows w ON w.tenant_id=r.tenant_id AND w.id=NEW.window_id
        JOIN agent_accounts ledger ON ledger.tenant_id=w.tenant_id AND ledger.id=w.account_id
        WHERE r.tenant_id=NEW.tenant_id AND r.id=NEW.run_id
          AND (w.account_id=r.account_id OR
               (r.purpose='managed' AND NOT w.pairing_verification
                AND door.quota_fingerprint<>'' AND ledger.quota_fingerprint=door.quota_fingerprint
                AND ledger.harness=door.harness))
    ) THEN
        RAISE EXCEPTION 'reservation account does not match run quota';
    END IF;
    RETURN NEW;
END;
$$;
