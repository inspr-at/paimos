-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1519: set-based canonical leaf flags for list shape filters.
-- Budgets on aeon_work_scope stay unchanged. Visibility is widened only
-- inside this function and restored before it returns.
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION aeon_work_leaf_flags(targets uuid[])
RETURNS TABLE(id uuid, is_leaf boolean)
LANGUAGE plpgsql STABLE AS $$
DECLARE prior_visible text := current_setting('aeon.visible_projects', true);
 prior_system text := current_setting('aeon.system', true);
BEGIN
 PERFORM set_config('aeon.visible_projects', '*', true), set_config('aeon.system', 'on', true);
 RETURN QUERY
 SELECT n.id,
        NOT EXISTS (
            SELECT 1 FROM nodes c
            JOIN node_kinds ck ON ck.tenant_id = c.tenant_id AND ck.id = c.kind_id
            WHERE c.tenant_id = current_setting('aeon.tenant_id')::uuid
              AND c.parent_id = n.id
              AND c.deleted_at IS NULL
              AND ck.slug IN ('work', 'epic', 'ticket', 'task')
        )
 FROM nodes n
 JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
 WHERE n.tenant_id = current_setting('aeon.tenant_id')::uuid
   AND n.id = ANY (targets)
   AND n.deleted_at IS NULL
   AND k.slug IN ('work', 'epic', 'ticket', 'task');
 PERFORM set_config('aeon.visible_projects', coalesce(prior_visible, ''), true),
         set_config('aeon.system', coalesce(prior_system, ''), true);
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.visible_projects', coalesce(prior_visible, ''), true),
         set_config('aeon.system', coalesce(prior_system, ''), true);
 RAISE;
END;
$$;
