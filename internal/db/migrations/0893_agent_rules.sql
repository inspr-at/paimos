-- SPDX-License-Identifier: AGPL-3.0-only
-- ADR-004 rule resources remain nodes. The dedicated API is the only mutation
-- path, including for generic node bulk/move/undo. Published versions are frozen.
-- Refuse a namespace collision instead of interpreting pre-existing content as
-- published rules. This migration does not seed or publish any company rules.
DO $$
DECLARE tenant uuid; prior_tenant text := current_setting('aeon.tenant_id',true);
BEGIN
 FOR tenant IN SELECT id FROM tenants LOOP
  PERFORM set_config('aeon.tenant_id',tenant::text,true);
  IF EXISTS (SELECT 1 FROM node_kinds WHERE slug IN ('aeon_rule_layer','aeon_rule_set','aeon_rule','aeon_rule_version')) THEN
   RAISE EXCEPTION 'reserved rule kind already exists; reconcile explicitly before migration';
  END IF;
 END LOOP;
 PERFORM set_config('aeon.tenant_id',coalesce(prior_tenant,''),true);
END $$;
-- A small trigger-maintained column keeps every ordinary node read off the
-- JSONB/TOAST payload and avoids a correlated kind lookup in the RLS hot path.
ALTER TABLE nodes ADD COLUMN rule_resource text
 CHECK (rule_resource IN ('layer','set','rule','version'));
CREATE INDEX nodes_rule_resource_idx ON nodes(tenant_id,rule_resource,parent_id)
 WHERE rule_resource IS NOT NULL;

CREATE FUNCTION aeon_rule_visible(p_fields jsonb) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT coalesce(current_setting('aeon.rules_access',true),'')='on'
 AND coalesce(p_fields->'scope'->>'layer','') IN ('company','project','person','agent')
 AND (coalesce(p_fields->'scope'->>'owner_id','')=''
      OR p_fields->'scope'->>'owner_id'=current_setting('aeon.rules_owner',true))
 AND (coalesce(p_fields->'scope'->>'agent_id','')=''
      OR coalesce(current_setting('aeon.rules_agent',true),'')=''
      OR p_fields->'scope'->>'agent_id'=current_setting('aeon.rules_agent',true))
 AND (coalesce(p_fields->'scope'->>'project_id','')=''
      OR current_setting('aeon.rules_projects',true)='*'
      OR (p_fields->'scope'->>'project_id')=ANY(CASE WHEN coalesce(current_setting('aeon.rules_projects',true),'') IN ('','*') THEN '{}'::text[] ELSE current_setting('aeon.rules_projects',true)::text[] END))
$$;
CREATE POLICY nodes_rule_access ON nodes AS RESTRICTIVE
 USING (rule_resource IS NULL OR aeon_rule_visible(fields))
 WITH CHECK (rule_resource IS NULL OR aeon_rule_visible(fields));
CREATE POLICY events_rule_access ON events AS RESTRICTIVE FOR SELECT
 USING (type NOT LIKE 'rules.%' OR
   (coalesce(current_setting('aeon.rules_access',true),'')='on'
    AND EXISTS (SELECT 1 FROM nodes n WHERE n.tenant_id=events.tenant_id AND n.id=events.node_id)));

CREATE FUNCTION aeon_guard_rule_node() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_rule boolean := false; resource text; slug text;
BEGIN
 IF TG_OP <> 'INSERT' THEN old_rule := OLD.rule_resource IS NOT NULL; END IF;
 IF TG_OP <> 'DELETE' THEN
   IF TG_OP='INSERT' OR NEW.kind_id IS DISTINCT FROM OLD.kind_id THEN
     SELECT k.slug INTO slug FROM node_kinds k WHERE k.tenant_id=NEW.tenant_id AND k.id=NEW.kind_id;
     resource := CASE slug WHEN 'aeon_rule_layer' THEN 'layer' WHEN 'aeon_rule_set' THEN 'set'
                  WHEN 'aeon_rule' THEN 'rule' WHEN 'aeon_rule_version' THEN 'version' ELSE NULL END;
   ELSE
     resource := OLD.rule_resource;
   END IF;
   IF resource IS NULL AND (NEW.rule_resource IS NOT NULL OR NEW.fields ? '_aeon_rule_resource') THEN
     RAISE EXCEPTION 'reserved rule storage envelope' USING ERRCODE='42501';
   END IF;
   IF resource IS NOT NULL AND NEW.fields->>'_aeon_rule_resource' IS DISTINCT FROM resource THEN
     RAISE EXCEPTION 'rule storage envelope is required' USING ERRCODE='23514';
   END IF;
   NEW.rule_resource := resource;
 END IF;
 IF old_rule OR resource IS NOT NULL THEN
   IF coalesce(current_setting('aeon.rules_write',true),'') <> 'on' THEN
     RAISE EXCEPTION 'rule resources require the rules API' USING ERRCODE='42501';
   END IF;
   IF TG_OP <> 'INSERT' THEN
     IF OLD.rule_resource='version' THEN
       RAISE EXCEPTION 'published rule versions are immutable' USING ERRCODE='23514';
     END IF;
     IF TG_OP='UPDATE' AND (NEW.kind_id IS DISTINCT FROM OLD.kind_id OR NEW.parent_id IS DISTINCT FROM OLD.parent_id
         OR NEW.fields->'scope' IS DISTINCT FROM OLD.fields->'scope') THEN
       RAISE EXCEPTION 'rule identity and scope are immutable' USING ERRCODE='23514';
     END IF;
   END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER nodes_rules_guard BEFORE INSERT OR UPDATE OR DELETE ON nodes
 FOR EACH ROW EXECUTE FUNCTION aeon_guard_rule_node();

CREATE FUNCTION aeon_guard_rule_kind() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (TG_OP <> 'INSERT' AND OLD.slug IN ('aeon_rule_layer','aeon_rule_set','aeon_rule','aeon_rule_version'))
 OR (TG_OP <> 'DELETE' AND NEW.slug IN ('aeon_rule_layer','aeon_rule_set','aeon_rule','aeon_rule_version')) THEN
   IF TG_OP <> 'INSERT' OR coalesce(current_setting('aeon.rules_write',true),'') <> 'on' THEN
     RAISE EXCEPTION 'reserved rule kind' USING ERRCODE='42501';
   END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER node_kinds_rules_guard BEFORE INSERT OR UPDATE OR DELETE ON node_kinds
 FOR EACH ROW EXECUTE FUNCTION aeon_guard_rule_kind();

-- App writes marker and resource type only through the guarded reserved kinds.
CREATE UNIQUE INDEX rule_layer_scope_unique ON nodes(tenant_id,(fields->'scope'))
 WHERE rule_resource='layer' AND deleted_at IS NULL;
CREATE UNIQUE INDEX rule_snapshot_version_unique ON nodes(tenant_id,parent_id,(fields->>'version'))
 WHERE rule_resource='version';
CREATE UNIQUE INDEX rule_identity_unique ON nodes(tenant_id,parent_id,(fields->'rule'->>'identity'))
 WHERE rule_resource='rule' AND deleted_at IS NULL;
