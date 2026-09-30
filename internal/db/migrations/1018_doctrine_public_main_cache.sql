-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-366: verified main-tree metadata for network-free quotation exemptions.
SET LOCAL lock_timeout = '5s';

CREATE TABLE doctrine_public_main_cache (
    tenant_id uuid NOT NULL,
    source_id uuid NOT NULL,
    pin_commit text NOT NULL CHECK (pin_commit ~ '^[0-9a-f]{40}$'),
    main_commit text NOT NULL CHECK (main_commit ~ '^[0-9a-f]{40}$'),
    observed_at timestamptz NOT NULL,
    tree jsonb NOT NULL CHECK (jsonb_typeof(tree) = 'array'),
    PRIMARY KEY (tenant_id, source_id),
    FOREIGN KEY (tenant_id, source_id) REFERENCES doctrine_sources(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE doctrine_public_main_cache ENABLE ROW LEVEL SECURITY;
ALTER TABLE doctrine_public_main_cache FORCE ROW LEVEL SECURITY;
CREATE POLICY doctrine_public_main_cache_tenant ON doctrine_public_main_cache
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
