-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

-- Nullable for older writers; NULL is scoped mode, just like false.
ALTER TABLE agent_keys ADD COLUMN full_access boolean DEFAULT false;

-- AEON-578 persisted no preset marker. The coordinator-approved fallback is
-- the exact key name workstation-agents in each tenant, on an active ordinary
-- agent. Mark all non-revoked keys of that name (including expired keys so a
-- later manual rotation preserves their mode). No other name is inferred from
-- a historical scope list. Keep legacy scopes intact for old-release rollback;
-- new authorization ignores that list whenever full_access is true.
-- Tenant context must be set for every row because agent_keys forces RLS.
-- The migration runner repeats this additive statement under each tenant's
-- RLS context in the same transaction as the column expansion.
UPDATE agent_keys k SET full_access = true
WHERE k.name='workstation-agents' AND k.revoked_at IS NULL
  AND EXISTS (SELECT 1 FROM principals p
    WHERE p.tenant_id=k.tenant_id AND p.id=k.principal_id
      AND p.kind='agent' AND p.status='active'
      AND NOT (p.roles && ARRAY['system','importer','operator','embedding','quote_public_service','quote_confirmation_service','portal_public_service']::text[]));
