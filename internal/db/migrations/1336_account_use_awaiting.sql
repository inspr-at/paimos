-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1173: an account added while New accounts asks first waits for a person.
-- The mark is written with the account and cleared by the first person save that
-- touches the account's cells; GET /account-use reports an account as awaiting
-- while it has the mark and no allowed cell. Accounts added before this release
-- carry no mark.
SET LOCAL lock_timeout = '5s';

CREATE TABLE account_use_awaiting (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    account_id uuid NOT NULL,
    rule_revision bigint NOT NULL,
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id,account_id),
    FOREIGN KEY (tenant_id,account_id) REFERENCES agent_accounts(tenant_id,id) ON DELETE CASCADE
);
ALTER TABLE account_use_awaiting ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_use_awaiting FORCE ROW LEVEL SECURITY;
CREATE POLICY account_use_awaiting_tenant ON account_use_awaiting
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

-- Same conditions as the stamp trigger of 1309: it runs in the inserting
-- tenant and takes no advisory locks.
CREATE FUNCTION aeon_account_use_mark_awaiting() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO account_use_awaiting(tenant_id,account_id,rule_revision)
        SELECT NEW.tenant_id,NEW.id,r.revision FROM account_use_rules r
        WHERE r.tenant_id=NEW.tenant_id AND r.new_accounts='ask';
    RETURN NULL;
END $$;
CREATE TRIGGER agent_accounts_use_awaiting AFTER INSERT ON agent_accounts
    FOR EACH ROW EXECUTE FUNCTION aeon_account_use_mark_awaiting();
