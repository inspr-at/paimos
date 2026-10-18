-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1086 / S06 / M3. Append-only policy revisions and evaluation bindings.
-- No existing writer gains an obligation or execution authority.
SET LOCAL lock_timeout = '5s';
CREATE TABLE routine_guard_policies (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 scope_kind text NOT NULL CHECK (scope_kind IN ('tenant','project','user')),
 scope_id uuid NOT NULL,
 revision bigint NOT NULL CHECK (revision>0),
 rules jsonb NOT NULL CHECK (jsonb_typeof(rules)='array' AND jsonb_array_length(rules)<=64 AND octet_length(rules::text)<=131072),
 loosenings jsonb NOT NULL CHECK (jsonb_typeof(loosenings)='array' AND jsonb_array_length(loosenings)<=64 AND octet_length(loosenings::text)<=262144),
 created_by uuid NOT NULL,
 reason text NOT NULL CHECK (octet_length(reason)<=2048),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id,scope_kind,scope_id,revision),
 FOREIGN KEY (tenant_id,created_by) REFERENCES principals(tenant_id,id)
);
ALTER TABLE routine_guard_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE routine_guard_policies FORCE ROW LEVEL SECURITY;
CREATE POLICY routine_guard_policies_tenant ON routine_guard_policies
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY routine_guard_policies_scope ON routine_guard_policies AS RESTRICTIVE
 USING ((SELECT aeon_visibility_system()) OR scope_kind='tenant'
  OR (scope_kind='project' AND ((SELECT aeon_visible_all()) OR scope_id=ANY((SELECT aeon_visible_projects())::uuid[])))
  OR (scope_kind='user' AND scope_id=ANY((SELECT aeon_current_principals())::uuid[])))
 WITH CHECK ((SELECT aeon_visibility_system()) OR scope_kind='tenant'
  OR (scope_kind='project' AND ((SELECT aeon_visible_all()) OR scope_id=ANY((SELECT aeon_visible_projects())::uuid[])))
  OR (scope_kind='user' AND scope_id=ANY((SELECT aeon_current_principals())::uuid[])));
CREATE INDEX routine_guard_policies_latest ON routine_guard_policies(tenant_id,scope_kind,scope_id,revision DESC);

CREATE TABLE routine_guard_evaluations (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 recurrence_id uuid,
 scope_kind text NOT NULL CHECK (scope_kind IN ('project','personal','workspace')),
 scope_project_id uuid,
 owner_principal_id uuid NOT NULL,
 output_project_id uuid NOT NULL,
 definition_revision bigint NOT NULL,
 checkpoint text NOT NULL CHECK (checkpoint IN ('save','action')),
 policy_digest text NOT NULL,
 context_digest text NOT NULL,
 hard_version text NOT NULL,
 preset_version text NOT NULL,
 result text NOT NULL CHECK (result IN ('allow','needs_person','block')),
 required_evaluation boolean NOT NULL,
 decision jsonb NOT NULL CHECK (jsonb_typeof(decision)='object' AND octet_length(decision::text)<=262144),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id,id),
 FOREIGN KEY (tenant_id,recurrence_id) REFERENCES recurrences(tenant_id,id),
 FOREIGN KEY (tenant_id,scope_project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,owner_principal_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,output_project_id) REFERENCES nodes(tenant_id,id)
);
ALTER TABLE routine_guard_evaluations ENABLE ROW LEVEL SECURITY;
ALTER TABLE routine_guard_evaluations FORCE ROW LEVEL SECURITY;
CREATE POLICY routine_guard_evaluations_tenant ON routine_guard_evaluations
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY routine_guard_evaluations_scope ON routine_guard_evaluations AS RESTRICTIVE
 USING (aeon_routine_scope_visible(scope_kind,scope_project_id,owner_principal_id)
  AND ((SELECT aeon_visible_all()) OR output_project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
  AND (recurrence_id IS NULL OR EXISTS(SELECT 1 FROM recurrence_definitions d WHERE d.tenant_id=routine_guard_evaluations.tenant_id AND d.recurrence_id=routine_guard_evaluations.recurrence_id)))
 WITH CHECK (aeon_routine_scope_visible(scope_kind,scope_project_id,owner_principal_id)
  AND ((SELECT aeon_visible_all()) OR output_project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
  AND (recurrence_id IS NULL OR EXISTS(SELECT 1 FROM recurrence_definitions d WHERE d.tenant_id=routine_guard_evaluations.tenant_id AND d.recurrence_id=routine_guard_evaluations.recurrence_id)));
CREATE INDEX routine_guard_evaluations_page ON routine_guard_evaluations(tenant_id,recurrence_id,created_at DESC,id DESC);
