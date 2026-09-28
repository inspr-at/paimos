-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-125. Public product portal.
-- Catalog, wishes and the product are tenant-configured node kinds (ADR-001).
-- portal_settings is off when the row is missing and when enabled is false.
-- portal_votes stores a ballot hash, never a name, address or account.
-- Anonymous abuse windows reuse quote_public_rate_limits (AEON-133).

CREATE TABLE portal_settings (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
    enabled boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
ALTER TABLE portal_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY portal_settings_tenant ON portal_settings
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
COMMENT ON TABLE portal_settings IS 'Public portal exposure for one tenant. Off unless enabled is true.';

CREATE TABLE portal_votes (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    wish_id uuid NOT NULL,
    voter_hash text NOT NULL CHECK (voter_hash ~ '^[0-9a-f]{64}$'),
    weight smallint NOT NULL DEFAULT 1 CHECK (weight IN (1, 3)),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, wish_id, voter_hash),
    FOREIGN KEY (tenant_id, wish_id) REFERENCES nodes(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE portal_votes ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_votes FORCE ROW LEVEL SECURITY;
CREATE POLICY portal_votes_tenant ON portal_votes
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
COMMENT ON COLUMN portal_votes.voter_hash IS 'SHA-256 of an HttpOnly ballot and the wish id. Not an address, name or account.';

-- Portal kinds for new tenants go through the starter seeder. Existing tenants
-- are backfilled below with only these three rows, so a deleted starter kind
-- is not inserted again.
CREATE OR REPLACE FUNCTION aeon_seed_portal_kinds(p_tenant_id uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    IF p_tenant_id IS DISTINCT FROM NULLIF(current_setting('aeon.tenant_id', true), '')::uuid THEN
        RAISE EXCEPTION 'tenant setting does not match portal-kind tenant';
    END IF;
    INSERT INTO node_kinds (tenant_id, slug, label, short_prefix, icon, field_schema)
    VALUES
        (p_tenant_id, 'portal_product', 'Portal product', 'PPR', 'catalog', '{}'::jsonb),
        (p_tenant_id, 'portal_feature', 'Portal feature', 'PCF', 'catalog', jsonb_build_object(
            'type','object',
            'properties', jsonb_build_object(
                'live_since', jsonb_build_object('type','string','description','Public calendar version once the feature is live.'),
                'legal_basis', jsonb_build_object('type','string','description','Short public legal basis.'),
                'decline_reason', jsonb_build_object('type','string','description','Public reason when the feature is declined.')
            )
        )),
        (p_tenant_id, 'portal_wish', 'Portal wish', 'PWS', 'star', '{}'::jsonb)
    ON CONFLICT (tenant_id, slug) DO NOTHING;
END;
$$;

CREATE OR REPLACE FUNCTION aeon_seed_node_kinds(p_tenant_id uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    IF p_tenant_id IS DISTINCT FROM NULLIF(current_setting('aeon.tenant_id', true), '')::uuid THEN
        RAISE EXCEPTION 'tenant setting does not match starter-kind tenant';
    END IF;
    INSERT INTO node_kinds (tenant_id, slug, label, short_prefix, icon, field_schema)
    VALUES
        (p_tenant_id, 'project',   'Project',   'PRJ', 'project',   '{}'::jsonb),
        (p_tenant_id, 'epic',      'Epic',      'EPC', 'epic',      '{}'::jsonb),
        (p_tenant_id, 'ticket',    'Ticket',    'TKT', 'ticket',    jsonb_build_object('type','object','properties',aeon_ticket_benefit_properties())),
        (p_tenant_id, 'task',      'Task',      'TSK', 'task',      '{}'::jsonb),
        (p_tenant_id, 'release',   'Release',   'REL', 'release',   '{}'::jsonb),
        (p_tenant_id, 'memory',    'Memory',    'MEM', 'memory',    '{}'::jsonb),
        (p_tenant_id, 'runbook',   'Runbook',   'RUN', 'runbook',   '{}'::jsonb),
        (p_tenant_id, 'guideline', 'Guideline', 'GUI', 'guideline', '{}'::jsonb)
    ON CONFLICT (tenant_id, slug) DO NOTHING;
    PERFORM aeon_seed_portal_kinds(p_tenant_id);
END;
$$;

DO $$
DECLARE
    t record;
    prior_setting text := current_setting('aeon.tenant_id', true);
BEGIN
    FOR t IN SELECT id FROM tenants LOOP
        PERFORM set_config('aeon.tenant_id', t.id::text, true);
        PERFORM aeon_seed_portal_kinds(t.id);
    END LOOP;
    PERFORM set_config('aeon.tenant_id', coalesce(prior_setting, ''), true);
END;
$$;
