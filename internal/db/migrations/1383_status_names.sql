-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1401: a workspace's own words for its statuses, per UI locale.
-- Expansion only: a new table older binaries neither read nor write. Stored
-- states, transitions and checks stay canonical; this is display only.
SET LOCAL lock_timeout = '5s';
CREATE TABLE status_names (
 tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
 names jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(names) = 'object'),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
ALTER TABLE status_names ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_names FORCE ROW LEVEL SECURITY;
CREATE POLICY status_names_tenant ON status_names
 USING (tenant_id = current_setting('aeon.tenant_id')::uuid)
 WITH CHECK (tenant_id = current_setting('aeon.tenant_id')::uuid);
