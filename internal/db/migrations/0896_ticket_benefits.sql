-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-256. Tenant default, all projects; no node values or history rewritten.
-- No required array: drafts warn and application transitions enforce completion.
CREATE FUNCTION aeon_ticket_benefit_properties() RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$ SELECT '{"pill_en": {"type": "string", "description": "English pill, 2–4 words; required before done."}, "pill_de": {"type": "string", "description": "German pill, 2–4 words; required before done."}, "benefit_en": {"type": "string", "description": "One or two plain-language sentences about the user benefit; required before done."}, "benefit_de": {"type": "string", "description": "Ein oder zwei einfache Sätze zum Nutzen, neutral ohne direkte Ansprache; vor done erforderlich."}, "hide_from_release_notes": {"type": "boolean", "default": false, "description": "Omit from release notes; benefit fields are still required before done."}}'::jsonb $$;

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
        (p_tenant_id, 'ticket',    'Ticket',    'TKT', 'ticket',    jsonb_build_object('type','object','properties',aeon_ticket_benefit_properties())),
        (p_tenant_id, 'task',      'Task',      'TSK', 'task',      '{}'::jsonb),
        (p_tenant_id, 'release',   'Release',   'REL', 'release',   '{}'::jsonb),
        (p_tenant_id, 'memory',    'Memory',    'MEM', 'memory',    '{}'::jsonb),
        (p_tenant_id, 'runbook',   'Runbook',   'RUN', 'runbook',   '{}'::jsonb),
        (p_tenant_id, 'guideline', 'Guideline', 'GUI', 'guideline', '{}'::jsonb)
    ON CONFLICT (tenant_id, slug) DO NOTHING;
END;
$$;

DO $$
DECLARE
 t record;
 prior_setting text := current_setting('aeon.tenant_id', true);
BEGIN
 FOR t IN SELECT id FROM tenants LOOP
  PERFORM set_config('aeon.tenant_id', t.id::text, true);
  -- Keep custom properties and all other schema constraints. These five names
  -- are now the tenant-default contract; their prior values are not backfilled.
  UPDATE node_kinds SET field_schema=jsonb_set(field_schema, '{properties}',
   coalesce(field_schema->'properties', '{}'::jsonb) || aeon_ticket_benefit_properties()), updated_at=now()
   WHERE tenant_id=t.id AND slug='ticket';
 END LOOP;
 PERFORM set_config('aeon.tenant_id', coalesce(prior_setting,''), true);
END;
$$;
