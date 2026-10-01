-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-397: self-reported fingerprints are hints, never pooling authority.
-- Existing enrollments start unconfirmed. The person-only API records consent.
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_accounts ADD COLUMN quota_pool_fingerprint text NOT NULL DEFAULT ''
    CHECK (quota_pool_fingerprint = '' OR
           (quota_pool_fingerprint = quota_fingerprint AND quota_pool_fingerprint ~ '^[a-f0-9]{64}$'));
CREATE INDEX agent_accounts_confirmed_quota ON agent_accounts(tenant_id, harness, quota_pool_fingerprint)
    WHERE quota_pool_fingerprint <> '';
-- The primary keys cover ticket/run cascades; these cover the reverse lookups.
CREATE INDEX account_run_targets_group ON account_run_targets(tenant_id, group_id);
CREATE INDEX account_ticket_pins_group ON account_ticket_pins(tenant_id, group_id) WHERE group_id IS NOT NULL;
CREATE INDEX account_ticket_pins_account ON account_ticket_pins(tenant_id, account_id) WHERE account_id IS NOT NULL;

CREATE OR REPLACE FUNCTION aeon_guard_account_reservation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    -- A prior hold remains an accounting obligation after consent is revoked
    -- or a signal changes. Release/settle its original ledger, but never
    -- reactivate it or move it to another run/window without the current guard.
    IF TG_OP = 'UPDATE' AND OLD.tenant_id = NEW.tenant_id AND OLD.run_id = NEW.run_id
       AND OLD.window_id = NEW.window_id AND OLD.state = 'active'
       AND NEW.state IN ('released', 'settled') THEN
        RETURN NEW;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM agent_runs r
        JOIN agent_accounts door ON door.tenant_id=r.tenant_id AND door.id=r.account_id
        JOIN account_allowance_windows w ON w.tenant_id=r.tenant_id AND w.id=NEW.window_id
        JOIN agent_accounts ledger ON ledger.tenant_id=w.tenant_id AND ledger.id=w.account_id
        WHERE r.tenant_id=NEW.tenant_id AND r.id=NEW.run_id
          AND (w.account_id=r.account_id OR
               (r.purpose='managed' AND NOT w.pairing_verification
                AND door.quota_pool_fingerprint<>'' AND ledger.quota_pool_fingerprint=door.quota_pool_fingerprint
                AND ledger.harness=door.harness))
    ) THEN
        RAISE EXCEPTION 'reservation account does not match run quota';
    END IF;
    RETURN NEW;
END;
$$;
