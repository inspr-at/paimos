-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

-- An intent is NOT an execution grant. Existing coordinators remain unrelated
-- until explicitly bound by ID, person ownership and their own worker lease.
CREATE TABLE project_leads (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    project_id uuid NOT NULL,
    owner_principal_id uuid NOT NULL,
    session_id uuid,
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    state text NOT NULL CHECK (state IN ('starting','working','waiting_for_room','paused','cannot_start')),
    reason text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, project_id),
    FOREIGN KEY (tenant_id, project_id) REFERENCES nodes(tenant_id,id),
    FOREIGN KEY (tenant_id, owner_principal_id) REFERENCES principals(tenant_id,id),
    FOREIGN KEY (tenant_id, project_id, session_id) REFERENCES harness_sessions(tenant_id,project_id,id),
    UNIQUE (tenant_id, session_id)
);
ALTER TABLE project_leads ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_leads FORCE ROW LEVEL SECURITY;
CREATE POLICY project_leads_tenant ON project_leads
    USING (tenant_id = nullif(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY project_leads_project ON project_leads AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    WITH CHECK ((SELECT aeon_visible_all()) OR project_id = ANY ((SELECT aeon_visible_projects())::uuid[]));

CREATE TABLE project_lead_generations (
    tenant_id uuid NOT NULL,
    project_id uuid NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    session_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, project_id, generation),
    UNIQUE (tenant_id, session_id),
    FOREIGN KEY (tenant_id, project_id) REFERENCES project_leads(tenant_id, project_id),
    FOREIGN KEY (tenant_id, project_id, session_id) REFERENCES harness_sessions(tenant_id, project_id, id)
);
ALTER TABLE project_lead_generations ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_lead_generations FORCE ROW LEVEL SECURITY;
CREATE POLICY project_lead_generations_tenant ON project_lead_generations
    USING (tenant_id = nullif(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY project_lead_generations_project ON project_lead_generations AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    WITH CHECK ((SELECT aeon_visible_all()) OR project_id = ANY ((SELECT aeon_visible_projects())::uuid[]));

CREATE FUNCTION aeon_lead_generation_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'lead generation history is immutable' USING ERRCODE='23514';
END;
$$;
CREATE TRIGGER project_lead_generations_immutable BEFORE UPDATE OR DELETE ON project_lead_generations
    FOR EACH ROW EXECUTE FUNCTION aeon_lead_generation_immutable();

-- Harness succession cannot move a bound lead implicitly, even after loss of
-- heartbeat. Worker assignments keep their original generation and parent.
-- A new, explicitly proven lead claim is the only ownership transition.
