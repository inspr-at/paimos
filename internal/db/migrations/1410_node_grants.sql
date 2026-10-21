-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1582: additive node reads through the existing principal entry contract.
-- Project visibility and every write check retain their previous meaning.
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION aeon_visible_nodes() RETURNS uuid[]
LANGUAGE sql STABLE AS $$
    SELECT coalesce(NULLIF(current_setting('aeon.visible_nodes', true), '')::uuid[], '{}'::uuid[])
$$;

CREATE FUNCTION aeon_principal_node_visibility(p_tenant uuid, p_principal uuid) RETURNS uuid[]
LANGUAGE sql STABLE AS $$
    SELECT coalesce(array_agg(DISTINCT b.scope_id ORDER BY b.scope_id), '{}'::uuid[])
    FROM principals p
    JOIN principals c ON c.tenant_id = p.tenant_id AND c.id = coalesce(p.linked_to, p.id)
    JOIN role_bindings b ON b.tenant_id = c.tenant_id AND b.principal_id = c.id
    WHERE p.tenant_id = p_tenant AND p.id = p_principal
      AND p.status = 'active' AND c.status = 'active'
      AND b.scope_type = 'node' AND aeon_role_reads_nodes(p_tenant, b.role_id, 'node')
$$;

-- Intersect sets of readable records, including project/node cross-terms.
-- Only explicit node candidates need a lookup; never enumerate a project.
-- Invoker rights and tenant/rule RLS remain active during the bounded lookup.
CREATE FUNCTION aeon_visibility_nodes_intersect(p_tenant uuid, a text, a_nodes uuid[], b text, b_nodes uuid[]) RETURNS uuid[]
LANGUAGE plpgsql AS $$
DECLARE
    prior text := current_setting('aeon.visible_projects', true);
    result uuid[];
BEGIN
    IF cardinality(a_nodes) = 0 AND cardinality(b_nodes) = 0 THEN
        RETURN '{}'::uuid[];
    END IF;
    PERFORM set_config('aeon.visible_projects', '*', true);
    SELECT coalesce(array_agg(n.id ORDER BY n.id), '{}'::uuid[]) INTO result
    FROM nodes n
    WHERE n.tenant_id = p_tenant AND n.id = ANY(a_nodes || b_nodes)
      AND (a = '*' OR n.project_id = ANY(CASE WHEN coalesce(a, '') IN ('', '*') THEN '{}'::uuid[] ELSE a::uuid[] END) OR n.id = ANY(a_nodes))
      AND (b = '*' OR n.project_id = ANY(CASE WHEN coalesce(b, '') IN ('', '*') THEN '{}'::uuid[] ELSE b::uuid[] END) OR n.id = ANY(b_nodes));
    PERFORM set_config('aeon.visible_projects', coalesce(prior, ''), true);
    RETURN result;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('aeon.visible_projects', coalesce(prior, ''), true);
    RAISE;
END;
$$;

-- The four-argument form owns both settings. The legacy entry delegates here
-- with no person, preserving its text result and the account-use guard.
CREATE OR REPLACE FUNCTION aeon_enter_principal(p_tenant uuid, p_principal uuid, p_key_creator uuid, p_acting_person uuid) RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    visibility text;
    visible_nodes uuid[];
    ceiling text;
    ceiling_nodes uuid[];
    canonical uuid;
BEGIN
    PERFORM set_config('aeon.tenant_id', p_tenant::text, true);
    PERFORM aeon_account_use_gate();
    PERFORM set_config('aeon.visible_projects', '', true);
    PERFORM set_config('aeon.visible_nodes', '', true);
    PERFORM set_config('aeon.principal_ids', '', true);
    PERFORM set_config('aeon.system', '', true);
    visibility := aeon_principal_visibility(p_tenant, p_principal);
    visible_nodes := aeon_principal_node_visibility(p_tenant, p_principal);
    IF p_key_creator IS NOT NULL THEN
        ceiling := aeon_principal_visibility(p_tenant, p_key_creator);
        ceiling_nodes := aeon_principal_node_visibility(p_tenant, p_key_creator);
        visible_nodes := aeon_visibility_nodes_intersect(p_tenant, visibility, visible_nodes, ceiling, ceiling_nodes);
        visibility := aeon_visibility_intersect(visibility, ceiling);
    END IF;
    IF p_acting_person IS NOT NULL THEN
        ceiling := aeon_person_visibility(p_tenant, p_acting_person);
        ceiling_nodes := '{}'::uuid[];
        IF EXISTS (SELECT 1 FROM principals p WHERE p.tenant_id = p_tenant AND p.id = p_acting_person AND p.kind = 'person') THEN
            ceiling_nodes := aeon_principal_node_visibility(p_tenant, p_acting_person);
        END IF;
        visible_nodes := aeon_visibility_nodes_intersect(p_tenant, visibility, visible_nodes, ceiling, ceiling_nodes);
        visibility := aeon_visibility_intersect(visibility, ceiling);
    END IF;
    SELECT coalesce(p.linked_to, p.id) INTO canonical FROM principals p
    WHERE p.tenant_id = p_tenant AND p.id = p_principal;
    PERFORM set_config('aeon.principal_ids', ARRAY[p_principal, coalesce(canonical, p_principal)]::text, true);
    PERFORM set_config('aeon.visible_projects', visibility, true);
    PERFORM set_config('aeon.visible_nodes', visible_nodes::text, true);
    RETURN visibility;
END;
$$;

CREATE OR REPLACE FUNCTION aeon_enter_principal(p_tenant uuid, p_principal uuid, p_key_creator uuid) RETURNS text
LANGUAGE sql AS $$
    SELECT aeon_enter_principal(p_tenant, p_principal, p_key_creator, NULL::uuid)
$$;

-- Alter only USING. The existing project/workspace WITH CHECK stays intact;
-- node grants never become write authority in either RLS checks or RequireTx.
--
-- A bare "id = ANY(visible nodes)" is a second index path. The 6000-node child
-- walk then BitmapOr's the primary key and misses its budget, even when the
-- grant list is empty. Keep that list a non-indexable constant false unless a
-- grant exists; membership itself is unchanged.
ALTER POLICY nodes_project_visibility ON nodes
    USING ((SELECT aeon_visible_all())
        OR project_id = ANY ((SELECT aeon_visible_projects())::uuid[])
        OR CASE (SELECT cardinality(aeon_visible_nodes()))
               WHEN 0 THEN false
               ELSE id = ANY ((SELECT aeon_visible_nodes())::uuid[])
           END);
