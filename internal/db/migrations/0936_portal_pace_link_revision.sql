-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-125. Release history is published only for the project and link
-- revision the admin still has on screen. Existing links start at 1.
-- portal_pace_revision keeps the highest number issued for the tenant.
-- Clearing portal_pace deletes the link and leaves this counter, so the
-- next link cannot reuse a revision a stale history write still holds.
SET LOCAL lock_timeout = '5s';

ALTER TABLE portal_pace
    ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision >= 1);

COMMENT ON COLUMN portal_pace.revision IS
    'Revision issued for this link. A history write matches this value and the project together. Clearing the row does not return the number.';

CREATE TABLE portal_pace_revision (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    revision bigint NOT NULL CHECK (revision >= 1)
);

-- Seed before row-level security, while this migration can see every tenant.
-- portal_pace has one row per tenant, so the primary key cannot conflict.
INSERT INTO portal_pace_revision (tenant_id, revision)
SELECT tenant_id, revision FROM portal_pace;

ALTER TABLE portal_pace_revision ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_pace_revision FORCE ROW LEVEL SECURITY;
CREATE POLICY portal_pace_revision_tenant ON portal_pace_revision
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

COMMENT ON TABLE portal_pace_revision IS
    'Highest pace-link revision issued for the tenant. Survives a cleared portal_pace row so the next link cannot reuse a revision a stale release-history write still holds.';
