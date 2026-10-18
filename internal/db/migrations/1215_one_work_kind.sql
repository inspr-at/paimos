-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-649. The runner reconciles tenants and migrates nodes in this same
-- transaction before recording this file. Rollback is the verified backup,
-- never node Undo. No historical session, desk identity or event is rewritten.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE OR REPLACE FUNCTION aeon_seed_node_kinds(p_tenant_id uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    IF p_tenant_id IS DISTINCT FROM NULLIF(current_setting('aeon.tenant_id', true), '')::uuid THEN
        RAISE EXCEPTION 'tenant setting does not match starter-kind tenant';
    END IF;
    INSERT INTO node_kinds (tenant_id, slug, label, short_prefix, icon, field_schema)
    VALUES
        (p_tenant_id, 'project', 'Project', 'PRJ', 'project', '{}'::jsonb),
        (p_tenant_id, 'work', 'Work item', 'TKT', 'ticket', jsonb_build_object(
            'type','object','issue_family',true,'properties',
            aeon_ticket_benefit_properties() || aeon_ticket_route_properties() ||
            aeon_ticket_roadmap_properties() || aeon_work_classification_properties() ||
            aeon_human_check_properties())),
        (p_tenant_id, 'release', 'Release', 'REL', 'release', '{}'::jsonb),
        (p_tenant_id, 'memory', 'Memory', 'MEM', 'memory', '{}'::jsonb),
        (p_tenant_id, 'runbook', 'Runbook', 'RUN', 'runbook', '{}'::jsonb),
        (p_tenant_id, 'guideline', 'Guideline', 'GUI', 'guideline', '{}'::jsonb)
    ON CONFLICT (tenant_id, slug) DO NOTHING;
    PERFORM aeon_seed_portal_kinds(p_tenant_id);
END;
$$;

-- Historical journey projections retain their identities and memberships.
-- New importer writes target work rather than recreating retired definitions.
CREATE OR REPLACE FUNCTION aeon_guard_journey_node_kind() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE target_id uuid; expected_slug text;
BEGIN
    CASE TG_TABLE_NAME
        WHEN 'journey_projects' THEN target_id := NEW.project_node_id; expected_slug := 'project';
        WHEN 'journey_releases' THEN target_id := NEW.release_node_id; expected_slug := 'release';
        WHEN 'journey_requirements' THEN target_id := NEW.requirement_node_id; expected_slug := 'requirement';
        WHEN 'journey_features' THEN target_id := NEW.feature_node_id; expected_slug := 'work';
        WHEN 'journey_tickets' THEN target_id := NEW.ticket_node_id; expected_slug := 'work';
    END CASE;
    IF NOT EXISTS (SELECT 1 FROM nodes n JOIN node_kinds k
        ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
        WHERE n.tenant_id=NEW.tenant_id AND n.id=target_id
          AND n.deleted_at IS NULL AND k.slug=expected_slug) THEN
        RAISE EXCEPTION '% requires a live % node', TG_TABLE_NAME, expected_slug;
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION aeon_extend_human_check_schema() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.slug IN ('ticket','task','work') THEN
  NEW.field_schema := jsonb_set(NEW.field_schema, '{properties}',
   coalesce(NEW.field_schema->'properties','{}'::jsonb) || aeon_human_check_properties());
 END IF;
 RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION aeon_extend_work_classification_schema() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.slug IN ('ticket','task','work') THEN
  NEW.field_schema := jsonb_set(NEW.field_schema, '{properties}',
   coalesce(NEW.field_schema->'properties','{}'::jsonb) || aeon_work_classification_properties());
 END IF;
 RETURN NEW;
END;
$$;
