-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-568: value-free, at-most-once recipient claims, independent of devices.
-- A network timeout is ambiguous: retain the claim rather than repeat a push.
CREATE TABLE desk_notification_claims (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 kind text NOT NULL CHECK (kind IN ('question','approval','action_request')),
 item_id uuid NOT NULL,
 revision bigint NOT NULL CHECK (revision>0),
 recipient_id uuid NOT NULL,
 claimed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 state text NOT NULL DEFAULT 'claimed' CHECK (state IN ('claimed','sent','failed','skipped')),
 completed_at timestamptz,
 PRIMARY KEY (tenant_id,kind,item_id,revision,recipient_id),
 FOREIGN KEY (tenant_id,recipient_id) REFERENCES principals(tenant_id,id)
);
ALTER TABLE desk_notification_claims ENABLE ROW LEVEL SECURITY;
ALTER TABLE desk_notification_claims FORCE ROW LEVEL SECURITY;
CREATE POLICY desk_notification_claims_tenant ON desk_notification_claims
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

-- Recipient scans and canonical source aliases use UUID comparisons.
CREATE INDEX desk_notification_claims_recipient
 ON desk_notification_claims(tenant_id,recipient_id,kind,item_id,revision);
