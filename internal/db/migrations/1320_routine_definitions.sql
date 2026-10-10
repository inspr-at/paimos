-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1080 / S03 / M1: optional definition projection; legacy writers and
-- project/parent constraints stay intact. No assignment grants execution.
SET LOCAL lock_timeout = '5s';

ALTER TABLE recurrences ADD COLUMN definition_scope text;

CREATE FUNCTION aeon_routine_scope_visible(scope_type text, scope_project uuid, owner_id uuid)
RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT (SELECT aeon_visibility_system()) OR CASE scope_type
  WHEN 'personal' THEN owner_id=ANY((SELECT aeon_current_principals())::uuid[])
  WHEN 'workspace' THEN (SELECT aeon_visible_all())
  WHEN 'project' THEN (SELECT aeon_visible_all()) OR scope_project=ANY((SELECT aeon_visible_projects())::uuid[])
  ELSE false END
$$;

CREATE TABLE recurrence_definitions (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 recurrence_id uuid NOT NULL,
 scope_type text NOT NULL CHECK (scope_type IN ('project','personal','workspace')),
 scope_project_id uuid,
 owner_principal_id uuid NOT NULL,
 output_project_id uuid NOT NULL,
 output_parent_id uuid NOT NULL,
 assignment jsonb CHECK (assignment IS NULL OR (jsonb_typeof(assignment)='object' AND octet_length(assignment::text)<=131072)),
 -- S04 provisions this identity and person consent under its own authority.
 execution_principal_id uuid,
 execute_consent boolean NOT NULL DEFAULT false,
 consent_revision bigint NOT NULL DEFAULT 0,
 consented_by_principal_id uuid,
 consented_at timestamptz,
 PRIMARY KEY (tenant_id,recurrence_id),
 FOREIGN KEY (tenant_id,recurrence_id) REFERENCES recurrences(tenant_id,id) DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY (tenant_id,scope_project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,owner_principal_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,output_project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,output_parent_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,execution_principal_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,consented_by_principal_id) REFERENCES principals(tenant_id,id),
 CHECK ((scope_type='project')=(scope_project_id IS NOT NULL))
);
ALTER TABLE recurrence_definitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE recurrence_definitions FORCE ROW LEVEL SECURITY;
CREATE POLICY recurrence_definitions_tenant ON recurrence_definitions
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY recurrence_definitions_scope ON recurrence_definitions AS RESTRICTIVE
 USING (aeon_routine_scope_visible(scope_type,scope_project_id,owner_principal_id)
  AND ((SELECT aeon_visible_all()) OR output_project_id=ANY((SELECT aeon_visible_projects())::uuid[])))
 WITH CHECK (aeon_routine_scope_visible(scope_type,scope_project_id,owner_principal_id)
  AND ((SELECT aeon_visible_all()) OR output_project_id=ANY((SELECT aeon_visible_projects())::uuid[])));
CREATE INDEX recurrence_definitions_scope_page ON recurrence_definitions(tenant_id,scope_type,scope_project_id,recurrence_id);
CREATE INDEX recurrence_definitions_owner_page ON recurrence_definitions(tenant_id,owner_principal_id,recurrence_id);

-- The nullable marker distinguishes a legacy definition from an RLS-hidden
-- projection. A hidden personal row can never be mistaken for a legacy row.
CREATE POLICY recurrences_definition_scope ON recurrences AS RESTRICTIVE
 USING (definition_scope IS NULL OR EXISTS(SELECT 1 FROM recurrence_definitions d
  WHERE d.tenant_id=recurrences.tenant_id AND d.recurrence_id=recurrences.id AND d.scope_type=recurrences.definition_scope))
 WITH CHECK (definition_scope IS NULL OR EXISTS(SELECT 1 FROM recurrence_definitions d
  WHERE d.tenant_id=recurrences.tenant_id AND d.recurrence_id=recurrences.id AND d.scope_type=recurrences.definition_scope));

-- Protect full snapshots even for workspace owners and /events/SSE consumers.
-- Existing event/reference policies still apply; legacy events are unchanged.
-- Bind old-binary snapshots (which omit definition) to the marked row as well.
CREATE POLICY events_routine_definition_scope ON events AS RESTRICTIVE FOR SELECT
 USING (type NOT IN ('recurrence.created','recurrence.updated','recurrence.paused','recurrence.resumed','recurrence.deleted')
  OR ((coalesce(aeon_uuid_or_null(metadata->>'recurrence_id'),aeon_uuid_or_null(after->>'id'),aeon_uuid_or_null(before->>'id')) IS NULL
       OR EXISTS(SELECT 1 FROM recurrences r WHERE r.tenant_id=events.tenant_id AND r.id=coalesce(aeon_uuid_or_null(metadata->>'recurrence_id'),aeon_uuid_or_null(after->>'id'),aeon_uuid_or_null(before->>'id'))))
   AND (after->'definition' IS NULL OR after->'definition'='null'::jsonb
       OR aeon_routine_scope_visible(after#>>'{definition,scope,kind}',aeon_uuid_or_null(after#>>'{definition,scope,project_id}'),aeon_uuid_or_null(after#>>'{definition,owner_principal_id}')))
   AND (before->'definition' IS NULL OR before->'definition'='null'::jsonb
       OR aeon_routine_scope_visible(before#>>'{definition,scope,kind}',aeon_uuid_or_null(before#>>'{definition,scope,project_id}'),aeon_uuid_or_null(before#>>'{definition,owner_principal_id}')))));
