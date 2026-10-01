-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-523: additive replacement; retain the legacy binder for older binaries.
-- Accepted invites define access explicitly, including project-only access.
CREATE FUNCTION aeon_bind_legacy_uninvited(target_tenant uuid,target_principal uuid)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE canonical_id uuid;
BEGIN
    SELECT coalesce(linked_to,id) INTO canonical_id FROM principals
    WHERE tenant_id=target_tenant AND id=target_principal AND kind='person';
    IF canonical_id IS NULL THEN RETURN; END IF;
    IF EXISTS (SELECT 1 FROM invites WHERE tenant_id=target_tenant
               AND accepted_by=canonical_id AND accepted_at IS NOT NULL) THEN
        RETURN;
    END IF;
    PERFORM aeon_bind_legacy_principal(target_tenant,target_principal);
END;
$$;
