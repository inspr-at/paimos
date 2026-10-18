-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-502 A: configurable placement kinds; existing Go area gates stay intact.
SET LOCAL lock_timeout = '5s';

CREATE TABLE work_kinds (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 slug text NOT NULL CHECK (slug ~ '^[a-z][a-z0-9-]{0,47}$'),
 label text NOT NULL CHECK (length(btrim(label)) BETWEEN 1 AND 60),
 hint text NOT NULL DEFAULT '' CHECK (length(hint) <= 120),
 project_id uuid,
 system text CHECK (system IN ('review','security','other')),
 position integer NOT NULL DEFAULT 0,
 archived_at timestamptz,
 created_by uuid, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id,id),
 FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,created_by) REFERENCES principals(tenant_id,id),
 CHECK (system IS NULL OR (project_id IS NULL AND archived_at IS NULL AND slug=system)),
 CHECK (slug NOT IN ('review','security','other') OR (system IS NOT NULL AND system=slug))
);
CREATE UNIQUE INDEX work_kinds_default_slug ON work_kinds(tenant_id,slug) WHERE project_id IS NULL AND archived_at IS NULL;
CREATE UNIQUE INDEX work_kinds_project_slug ON work_kinds(tenant_id,project_id,slug) WHERE project_id IS NOT NULL AND archived_at IS NULL;
ALTER TABLE work_kinds ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_kinds FORCE ROW LEVEL SECURITY;
CREATE POLICY work_kinds_tenant ON work_kinds
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

CREATE FUNCTION aeon_model_pref_live_project() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.project_id IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE n.tenant_id=NEW.tenant_id AND n.id=NEW.project_id AND k.slug='project' AND n.deleted_at IS NULL
 ) THEN
  RAISE EXCEPTION 'preference project must be a live project' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER work_kinds_project_guard BEFORE INSERT OR UPDATE OF project_id ON work_kinds
 FOR EACH ROW EXECUTE FUNCTION aeon_model_pref_live_project();

CREATE FUNCTION aeon_work_kind_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.system IS NOT NULL THEN RAISE EXCEPTION 'system kind is immutable' USING ERRCODE='23514'; END IF;
  RETURN OLD;
 END IF;
 IF TG_OP='UPDATE' AND OLD.system IS NOT NULL AND
  (NEW.slug,NEW.label,NEW.archived_at,NEW.project_id,NEW.system) IS DISTINCT FROM
  (OLD.slug,OLD.label,OLD.archived_at,OLD.project_id,OLD.system) THEN
  RAISE EXCEPTION 'system kind is immutable' USING ERRCODE='23514';
 END IF;
 IF NEW.slug IN ('review','security','other') AND (NEW.system IS DISTINCT FROM NEW.slug OR NEW.project_id IS NOT NULL OR NEW.archived_at IS NOT NULL) THEN
  RAISE EXCEPTION 'system kind cannot be recreated or archived' USING ERRCODE='23514';
 END IF;
 IF NEW.archived_at IS NULL THEN
  PERFORM pg_advisory_xact_lock(hashtextextended('aeon-work-kind:'||NEW.tenant_id||':'||NEW.slug,0));
  IF EXISTS (SELECT 1 FROM work_kinds k WHERE k.tenant_id=NEW.tenant_id AND k.slug=NEW.slug
   AND k.id<>NEW.id AND k.archived_at IS NULL AND
   ((NEW.project_id IS NULL AND k.project_id IS NOT NULL) OR (NEW.project_id IS NOT NULL AND k.project_id IS NULL))) THEN
   RAISE EXCEPTION 'work kind slug taken across lists' USING ERRCODE='23505';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER work_kinds_guard BEFORE INSERT OR UPDATE OR DELETE ON work_kinds
 FOR EACH ROW EXECUTE FUNCTION aeon_work_kind_guard();

CREATE FUNCTION aeon_seed_work_kinds(p_tenant_id uuid) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 IF p_tenant_id IS DISTINCT FROM NULLIF(current_setting('aeon.tenant_id',true),'')::uuid THEN
  RAISE EXCEPTION 'tenant setting does not match work-kind tenant';
 END IF;
 INSERT INTO work_kinds(tenant_id,slug,label,hint,system,position)
 SELECT p_tenant_id,v.* FROM (VALUES
  ('design','UI and UX','screens, layout, interaction',NULL::text,0),
  ('frontend','Frontend','components, state, tests',NULL,1),
  ('backend','Backend','APIs, data, migrations',NULL,2),
  ('full-stack','Full stack','both ends in one change',NULL,3),
  ('infra','Infrastructure','CI, Nix, deploys',NULL,4),
  ('docs','Docs and copy','README, guides, notes',NULL,5),
  ('security','Security and permissions','auth, secrets, sanitising; reviewed by the security ladder','security',6),
  ('review','Reviews','never the author''s family','review',7),
  ('other','Everything else','any area without its own row','other',8)
 ) v(slug,label,hint,system,position) ON CONFLICT DO NOTHING;
END;
$$;

-- Preserve every route property while replacing only area's enum.
CREATE OR REPLACE FUNCTION aeon_ticket_route_properties() RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
SELECT jsonb_build_object(
  'route_role', jsonb_build_object('type', jsonb_build_array('string','null'), 'enum', jsonb_build_array('scout','mechanical','build','build-hard','review-gate', NULL), 'description', 'Doctrine role for who should build this. The server sets source, by and at.'),
  'route_role_source', jsonb_build_object('type', jsonb_build_array('string','null'), 'enum', jsonb_build_array('agent','person', NULL), 'description', 'Acting principal kind. Must match the caller when supplied.'),
  'route_role_by', jsonb_build_object('type', jsonb_build_array('string','null'), 'description', 'Principal id. The server overwrites this.'),
  'route_role_at', jsonb_build_object('type', jsonb_build_array('string','null'), 'description', 'When the role was set, UTC. The server overwrites this.'),
  'area', jsonb_build_object('type', jsonb_build_array('string','null'), 'pattern', '^[a-z][a-z0-9-]{0,47}$', 'description', 'Planning area. The server sets source, by and at.'),
  'area_source', jsonb_build_object('type', jsonb_build_array('string','null'), 'enum', jsonb_build_array('agent','person', NULL), 'description', 'Acting principal kind. Must match the caller when supplied.'),
  'area_by', jsonb_build_object('type', jsonb_build_array('string','null'), 'description', 'Principal id. The server overwrites this.'),
  'area_at', jsonb_build_object('type', jsonb_build_array('string','null'), 'description', 'When the area was set, UTC. The server overwrites this.')
);
$$;
DO $$
DECLARE t record; prior text := current_setting('aeon.tenant_id',true);
BEGIN
 FOR t IN SELECT id FROM tenants LOOP
  PERFORM set_config('aeon.tenant_id',t.id::text,true);
  PERFORM aeon_seed_work_kinds(t.id);
  UPDATE node_kinds SET field_schema=jsonb_set(field_schema,'{properties}',
   coalesce(field_schema->'properties','{}'::jsonb)||jsonb_build_object('area',aeon_ticket_route_properties()->'area')),updated_at=now()
   WHERE tenant_id=t.id AND slug IN ('ticket','task');
 END LOOP;
 PERFORM set_config('aeon.tenant_id',coalesce(prior,''),true);
END;
$$;
