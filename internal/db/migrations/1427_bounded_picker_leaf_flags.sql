-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1696: an additive, bounded replacement for picker leaf probes.
-- Keep the original function available to older binaries.
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION aeon_work_leaf_flags_bounded(targets uuid[])
RETURNS TABLE(id uuid, is_leaf boolean)
LANGUAGE plpgsql STABLE AS $$
DECLARE prior_visible text := current_setting('aeon.visible_projects', true);
 prior_system text := current_setting('aeon.system', true);
BEGIN
 PERFORM set_config('aeon.visible_projects', '*', true), set_config('aeon.system', 'on', true);
 RETURN QUERY
 WITH wanted AS MATERIALIZED (
    SELECT DISTINCT t.id FROM unnest(targets) t(id) WHERE t.id IS NOT NULL
 ), work_kinds AS MATERIALIZED (
    SELECT k.id FROM node_kinds k
    WHERE k.tenant_id = current_setting('aeon.tenant_id')::uuid
      AND k.slug IN ('work', 'epic', 'ticket', 'task')
 ), work_parents AS MATERIALIZED (
    SELECT DISTINCT c.parent_id FROM nodes c
    WHERE c.tenant_id = current_setting('aeon.tenant_id')::uuid
      AND c.deleted_at IS NULL
      -- Keep both memberships hashed. Stale statistics must not turn this
      -- one canonical child pass into a tenant scan per candidate.
      AND (c.parent_id IN (SELECT w.id FROM wanted w)) IS TRUE
      AND (c.kind_id IN (SELECT k.id FROM work_kinds k)) IS TRUE
 )
 SELECT n.id,
        (n.id IN (SELECT p.parent_id FROM work_parents p)) IS NOT TRUE
 FROM wanted w CROSS JOIN LATERAL (
    SELECT candidate.id, candidate.kind_id FROM nodes candidate
    WHERE candidate.tenant_id = current_setting('aeon.tenant_id')::uuid
      AND candidate.id = w.id AND candidate.deleted_at IS NULL
      AND (candidate.tenant_id, candidate.id) >= (current_setting('aeon.tenant_id')::uuid, w.id)
      AND (candidate.tenant_id, candidate.id) <= (current_setting('aeon.tenant_id')::uuid, w.id)
    OFFSET 0
 ) n
 WHERE (n.kind_id IN (SELECT k.id FROM work_kinds k)) IS TRUE;
 PERFORM set_config('aeon.visible_projects', coalesce(prior_visible, ''), true),
         set_config('aeon.system', coalesce(prior_system, ''), true);
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.visible_projects', coalesce(prior_visible, ''), true),
         set_config('aeon.system', coalesce(prior_system, ''), true);
 RAISE;
END;
$$;
