-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-655: workspace vocabulary, independent of appearance/theme.
SET LOCAL lock_timeout = '5s';
CREATE TABLE work_vocabulary (
 tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
 revision bigint NOT NULL DEFAULT 1,
 vocabulary jsonb NOT NULL DEFAULT '{"leaf":{"name":"","icon":""},"levels":[]}'::jsonb
);
ALTER TABLE work_vocabulary ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_vocabulary FORCE ROW LEVEL SECURITY;
CREATE POLICY work_vocabulary_tenant ON work_vocabulary
 USING (tenant_id = current_setting('aeon.tenant_id')::uuid)
 WITH CHECK (tenant_id = current_setting('aeon.tenant_id')::uuid);

-- Only shape facts, never hidden child identities. Invoker rights preserve
-- project visibility. Canonical shape exposes only a boolean and depth;
-- counts below include only visible direct work children.
CREATE FUNCTION aeon_work_shape(target uuid)
RETURNS TABLE(is_leaf boolean,depth integer,work_children_count integer,status_derived boolean)
LANGUAGE plpgsql STABLE AS $$
DECLARE prior_visible text := current_setting('aeon.visible_projects',true);
 prior_system text := current_setting('aeon.system',true);
 enabled boolean; kind text; par uuid; project uuid; visible_children integer; canonical_parent boolean; d integer;
BEGIN
 SELECT k.slug,n.parent_id,n.project_id INTO kind,par,project FROM nodes n
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.id=target AND n.deleted_at IS NULL;
 IF kind IS NULL OR kind NOT IN ('work','epic','ticket','task') THEN RETURN; END IF;
 SELECT count(*)::int INTO visible_children FROM nodes c JOIN node_kinds k ON k.tenant_id=c.tenant_id AND k.id=c.kind_id
 WHERE c.tenant_id=current_setting('aeon.tenant_id')::uuid AND c.parent_id=target AND c.deleted_at IS NULL AND k.slug IN ('work','epic','ticket','task');
 -- The target was read under caller RLS first. Canonical shape must not change
 -- with visibility; only a boolean and depth cross this narrow boundary.
 PERFORM set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true);
 SELECT EXISTS(SELECT 1 FROM nodes c JOIN node_kinds k ON k.tenant_id=c.tenant_id AND k.id=c.kind_id
  WHERE c.tenant_id=current_setting('aeon.tenant_id')::uuid AND c.parent_id=target AND c.deleted_at IS NULL AND k.slug IN ('work','epic','ticket','task')) INTO canonical_parent;
 enabled := aeon_work_status_enabled(project);
 -- UNION fences cycles with linear storage, without growing path arrays.
 WITH RECURSIVE ancestors(id,parent_id,slug) AS (
  SELECT n.id,n.parent_id,k.slug FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE n.id=par AND n.deleted_at IS NULL
  UNION
  SELECT n.id,n.parent_id,k.slug FROM ancestors a JOIN nodes n ON n.id=a.parent_id
  JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE a.slug<>'project' AND n.deleted_at IS NULL
 ), bounded AS MATERIALIZED (SELECT * FROM ancestors LIMIT 50001)
 SELECT CASE WHEN count(*)>50000 THEN -1 ELSE 1+count(*) FILTER(WHERE slug IN ('work','epic','ticket','task'))::int END INTO d FROM bounded;
 IF d<0 THEN RAISE EXCEPTION 'work shape budget exceeded' USING ERRCODE='54000'; END IF;
 PERFORM set_config('aeon.visible_projects',coalesce(prior_visible,''),true),set_config('aeon.system',coalesce(prior_system,''),true);
 RETURN QUERY SELECT NOT canonical_parent,d,visible_children,canonical_parent AND enabled;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.visible_projects',coalesce(prior_visible,''),true),set_config('aeon.system',coalesce(prior_system,''),true);
 RAISE;
END;
$$;
