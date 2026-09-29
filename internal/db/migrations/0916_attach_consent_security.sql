-- SPDX-License-Identifier: AGPL-3.0-only
-- Per-person policy, never a daemon-supplied preference.
SET LOCAL lock_timeout = '5s';
CREATE TABLE person_watch_security (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 person_id uuid NOT NULL,
 consent_mode text NOT NULL DEFAULT 'aeon' CHECK (consent_mode IN ('aeon','local_auth')),
 PRIMARY KEY(tenant_id,person_id),
 FOREIGN KEY(tenant_id,person_id) REFERENCES principals(tenant_id,id)
);
ALTER TABLE person_watch_security ENABLE ROW LEVEL SECURITY;
ALTER TABLE person_watch_security FORCE ROW LEVEL SECURITY;
CREATE POLICY person_watch_security_tenant ON person_watch_security
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
-- Pending approvals read the current setting; approved/active rows keep this pin.
ALTER TABLE harness_attach_requests ADD COLUMN consent_mode text NOT NULL DEFAULT 'aeon'
 CHECK (consent_mode IN ('aeon','local_auth'));
