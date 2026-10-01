-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-523: classic alias history must not grant additional invited access.
CREATE OR REPLACE FUNCTION aeon_bind_legacy_principal(target_tenant uuid,target_principal uuid)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE chosen_role uuid;
DECLARE new_binding uuid;
DECLARE canonical_id uuid;
BEGIN
    SELECT coalesce(linked_to,id) INTO canonical_id FROM principals
    WHERE tenant_id=target_tenant AND id=target_principal AND kind='person';
    IF canonical_id IS NULL THEN RETURN; END IF;
    -- Accepted invitations define access explicitly, including project-only
    -- access. Neither sign-in nor a later classic import may resurrect the
    -- alias's legacy workspace permissions.
    IF EXISTS (SELECT 1 FROM invites WHERE tenant_id=target_tenant
               AND accepted_by=canonical_id AND accepted_at IS NOT NULL) THEN
        RETURN;
    END IF;
    SELECT r.id INTO chosen_role FROM (
      SELECT max(CASE
        WHEN 'super_admin'=ANY(p.roles) THEN 5
        WHEN 'admin'=ANY(p.roles) THEN 4
        WHEN 'member'=ANY(p.roles) OR 'reviewer'=ANY(p.roles) THEN 3
        WHEN 'external'=ANY(p.roles) THEN 2
        WHEN 'customer'=ANY(p.roles) THEN 1
        ELSE 0 END) AS rank
      FROM principals p WHERE p.tenant_id=target_tenant
        AND (p.id=canonical_id OR p.linked_to=canonical_id)
    ) mapped JOIN roles r ON r.tenant_id=target_tenant AND r.key=CASE mapped.rank
      WHEN 5 THEN 'owner' WHEN 4 THEN 'admin' WHEN 3 THEN 'member'
      WHEN 1 THEN 'customer' END;
    IF chosen_role IS NULL THEN RETURN; END IF;
    INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type)
      VALUES(target_tenant,canonical_id,chosen_role,'workspace')
      ON CONFLICT DO NOTHING RETURNING id INTO new_binding;
    IF new_binding IS NOT NULL THEN
      INSERT INTO events(tenant_id,actor_principal_id,type,after)
      VALUES(target_tenant,aeon_authz_system_actor(target_tenant),'authz.binding_migrated',
        jsonb_build_object('principal_id',canonical_id,'role_id',chosen_role,'scope_type','workspace'));
    END IF;
END;
$$;
