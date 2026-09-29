-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-329. route_role and area classify a ticket or task. Provenance columns
-- are server-written. Existing node values are not rewritten.
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION aeon_ticket_route_properties() RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
SELECT jsonb_build_object(
  'route_role', jsonb_build_object('type', jsonb_build_array('string','null'), 'enum', jsonb_build_array('scout','mechanical','build','build-hard','review-gate', NULL), 'description', 'Doctrine role for who should build this. The server sets source, by and at.'),
  'route_role_source', jsonb_build_object('type', jsonb_build_array('string','null'), 'enum', jsonb_build_array('agent','person', NULL), 'description', 'Acting principal kind. Must match the caller when supplied.'),
  'route_role_by', jsonb_build_object('type', jsonb_build_array('string','null'), 'description', 'Principal id. The server overwrites this.'),
  'route_role_at', jsonb_build_object('type', jsonb_build_array('string','null'), 'description', 'When the role was set, UTC. The server overwrites this.'),
  'area', jsonb_build_object('type', jsonb_build_array('string','null'), 'enum', jsonb_build_array('backend','frontend','full-stack','infra','design','docs', NULL), 'description', 'Planning area. The server sets source, by and at.'),
  'area_source', jsonb_build_object('type', jsonb_build_array('string','null'), 'enum', jsonb_build_array('agent','person', NULL), 'description', 'Acting principal kind. Must match the caller when supplied.'),
  'area_by', jsonb_build_object('type', jsonb_build_array('string','null'), 'description', 'Principal id. The server overwrites this.'),
  'area_at', jsonb_build_object('type', jsonb_build_array('string','null'), 'description', 'When the area was set, UTC. The server overwrites this.')
);
$$;

CREATE OR REPLACE FUNCTION aeon_seed_node_kinds(p_tenant_id uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    IF p_tenant_id IS DISTINCT FROM NULLIF(current_setting('aeon.tenant_id', true), '')::uuid THEN
        RAISE EXCEPTION 'tenant setting does not match starter-kind tenant';
    END IF;
    INSERT INTO node_kinds (tenant_id, slug, label, short_prefix, icon, field_schema)
    VALUES
        (p_tenant_id, 'project',   'Project',   'PRJ', 'project',   '{}'::jsonb),
        (p_tenant_id, 'epic',      'Epic',      'EPC', 'epic',      '{}'::jsonb),
        (p_tenant_id, 'ticket',    'Ticket',    'TKT', 'ticket',    jsonb_build_object('type','object','properties',aeon_ticket_benefit_properties() || aeon_ticket_route_properties())),
        (p_tenant_id, 'task',      'Task',      'TSK', 'task',      jsonb_build_object('type','object','properties',aeon_ticket_route_properties())),
        (p_tenant_id, 'release',   'Release',   'REL', 'release',   '{}'::jsonb),
        (p_tenant_id, 'memory',    'Memory',    'MEM', 'memory',    '{}'::jsonb),
        (p_tenant_id, 'runbook',   'Runbook',   'RUN', 'runbook',   '{}'::jsonb),
        (p_tenant_id, 'guideline', 'Guideline', 'GUI', 'guideline', '{}'::jsonb)
    ON CONFLICT (tenant_id, slug) DO NOTHING;
    PERFORM aeon_seed_portal_kinds(p_tenant_id);
END;
$$;

DO $$
DECLARE
 t record;
 prior_setting text := current_setting('aeon.tenant_id', true);
BEGIN
 FOR t IN SELECT id FROM tenants LOOP
  PERFORM set_config('aeon.tenant_id', t.id::text, true);
  UPDATE node_kinds SET field_schema=jsonb_set(field_schema, '{properties}',
   coalesce(field_schema->'properties', '{}'::jsonb) || aeon_ticket_route_properties()), updated_at=now()
   WHERE tenant_id=t.id AND slug IN ('ticket','task');
 END LOOP;
 PERFORM set_config('aeon.tenant_id', coalesce(prior_setting,''), true);
END;
$$;
