-- SPDX-License-Identifier: AGPL-3.0-only
-- Exact-byte migration-policy exception: retain the complete 0821 entry
-- contract and add an unconditional capability check immediately after tenant.
SET LOCAL lock_timeout = '5s';
CREATE OR REPLACE FUNCTION aeon_enter_principal(p_tenant uuid,p_principal uuid,p_key_creator uuid) RETURNS text
LANGUAGE plpgsql AS $$
DECLARE visibility text; canonical uuid;
BEGIN
    PERFORM set_config('aeon.tenant_id',p_tenant::text,true);
    PERFORM aeon_account_use_gate();
    PERFORM set_config('aeon.visible_projects','',true);
    PERFORM set_config('aeon.principal_ids','',true);
    PERFORM set_config('aeon.system','',true);
    visibility := aeon_principal_visibility(p_tenant,p_principal);
    IF p_key_creator IS NOT NULL THEN
        visibility := aeon_visibility_intersect(visibility,aeon_principal_visibility(p_tenant,p_key_creator));
    END IF;
    SELECT coalesce(p.linked_to,p.id) INTO canonical FROM principals p WHERE p.tenant_id=p_tenant AND p.id=p_principal;
    PERFORM set_config('aeon.principal_ids',ARRAY[p_principal,coalesce(canonical,p_principal)]::text,true);
    PERFORM set_config('aeon.visible_projects',visibility,true);
    RETURN visibility;
END $$;
CREATE POLICY agent_accounts_use_gate ON agent_accounts AS RESTRICTIVE
    USING ((SELECT aeon_account_use_gate())) WITH CHECK ((SELECT aeon_account_use_gate()));
