-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-431. A workspace's own brand in the header: a short name and a logo,
-- with an optional second logo for dark mode. Logos are small (at most
-- 256 KB) and are kept in the row, so tenant RLS guards the bytes as well.
-- SVG is stored only after the server's allowlist sanitizer rewrote it.
SET LOCAL lock_timeout = '5s';

CREATE TABLE tenant_brand (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
    short_name text NOT NULL DEFAULT '' CHECK (char_length(short_name) <= 32),
    updated_by_principal_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE tenant_brand ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_brand FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_brand_tenant ON tenant_brand
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

CREATE TABLE tenant_brand_logos (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    variant text NOT NULL CHECK (variant IN ('light', 'dark')),
    content_type text NOT NULL CHECK (content_type IN ('image/png', 'image/webp', 'image/svg+xml')),
    content bytea NOT NULL CHECK (octet_length(content) BETWEEN 1 AND 262144),
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    width integer NOT NULL CHECK (width BETWEEN 1 AND 4096),
    height integer NOT NULL CHECK (height BETWEEN 1 AND 4096),
    uploaded_by_principal_id uuid NOT NULL,
    uploaded_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, variant)
);
ALTER TABLE tenant_brand_logos ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_brand_logos FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_brand_logos_tenant ON tenant_brand_logos
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
