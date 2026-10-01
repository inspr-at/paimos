-- SPDX-License-Identifier: AGPL-3.0-only
-- Host-wide Aithema signer state, owned by the bootstrap tenant. The vault
-- binds the encrypted state to this owner and the signing-keyset purpose.
CREATE TABLE aithema_signing_keys (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    encrypted_state bytea NOT NULL
);
ALTER TABLE aithema_signing_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE aithema_signing_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY aithema_signing_keys_tenant ON aithema_signing_keys
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
