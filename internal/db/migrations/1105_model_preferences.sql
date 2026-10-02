-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
CREATE TABLE model_pref_scopes (
 tenant_id uuid NOT NULL REFERENCES tenants(id), id uuid NOT NULL DEFAULT gen_random_uuid(),
 level text NOT NULL CHECK (level IN ('default','person','project')),
 person_id uuid, project_id uuid,
 residency text CHECK (residency IN ('any','eu','local')),
 residency_locked boolean NOT NULL DEFAULT false, prefs_locked boolean NOT NULL DEFAULT false,
 revision bigint NOT NULL DEFAULT 1 CHECK (revision>0), updated_by uuid, updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id,id), UNIQUE (tenant_id,id,level),
 CHECK ((level='default' AND person_id IS NULL AND project_id IS NULL)
  OR (level='person' AND person_id IS NOT NULL AND project_id IS NULL)
  OR (level='project' AND project_id IS NOT NULL AND person_id IS NULL)),
 CHECK (level<>'project' OR NOT (residency_locked OR prefs_locked)),
 CHECK (NOT residency_locked OR residency IS NOT NULL),
 FOREIGN KEY (tenant_id,person_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,updated_by) REFERENCES principals(tenant_id,id)
);
CREATE UNIQUE INDEX model_pref_scopes_one ON model_pref_scopes(tenant_id,level,
 coalesce(person_id,'00000000-0000-0000-0000-000000000000'),coalesce(project_id,'00000000-0000-0000-0000-000000000000'));
CREATE TRIGGER model_pref_scopes_project_guard BEFORE INSERT OR UPDATE OF project_id ON model_pref_scopes
 FOR EACH ROW EXECUTE FUNCTION aeon_model_pref_live_project();
CREATE FUNCTION aeon_model_pref_person() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.person_id IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM principals p WHERE p.tenant_id=NEW.tenant_id AND p.id=NEW.person_id
  AND p.kind='person' AND p.status='active' AND p.linked_to IS NULL
 ) THEN RAISE EXCEPTION 'preference person must be canonical and active' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER model_pref_scopes_person_guard BEFORE INSERT OR UPDATE OF person_id ON model_pref_scopes
 FOR EACH ROW EXECUTE FUNCTION aeon_model_pref_person();

CREATE TABLE model_pref_rows (
 tenant_id uuid NOT NULL, scope_id uuid NOT NULL, kind_id uuid NOT NULL, level text NOT NULL,
 locked boolean NOT NULL DEFAULT false, updated_by uuid, updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id,scope_id,kind_id),
 FOREIGN KEY (tenant_id,scope_id,level) REFERENCES model_pref_scopes(tenant_id,id,level) ON DELETE CASCADE,
 FOREIGN KEY (tenant_id,kind_id) REFERENCES work_kinds(tenant_id,id),
 FOREIGN KEY (tenant_id,updated_by) REFERENCES principals(tenant_id,id),
 CHECK (level<>'project' OR NOT locked)
);
CREATE TABLE model_pref_cells (
 tenant_id uuid NOT NULL, scope_id uuid NOT NULL, kind_id uuid NOT NULL,
 bucket text NOT NULL CHECK (bucket IN ('normal','complex')),
 mode text NOT NULL CHECK (mode IN ('auto','latest','pinned')),
 profile_id uuid, family text, line text CHECK (line ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
 effort text CHECK (length(effort) BETWEEN 1 AND 32), harness text CHECK (harness IN ('codex','claude','pi','cursor','grok','gemini','opencode')),
 PRIMARY KEY (tenant_id,scope_id,kind_id,bucket),
 FOREIGN KEY (tenant_id,scope_id,kind_id) REFERENCES model_pref_rows(tenant_id,scope_id,kind_id) ON DELETE CASCADE,
 FOREIGN KEY (tenant_id,profile_id) REFERENCES model_profiles(tenant_id,id),
 CHECK ((mode='auto' AND profile_id IS NULL AND family IS NULL AND line IS NULL AND effort IS NULL AND harness IS NULL)
  OR (mode='latest' AND profile_id IS NULL AND family IS NOT NULL AND line IS NOT NULL AND effort IS NOT NULL)
  OR (mode='pinned' AND profile_id IS NOT NULL AND family IS NULL AND line IS NULL AND effort IS NULL AND harness IS NULL))
);
ALTER TABLE model_pref_scopes ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_pref_scopes FORCE ROW LEVEL SECURITY;
CREATE POLICY model_pref_scopes_tenant ON model_pref_scopes USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE model_pref_rows ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_pref_rows FORCE ROW LEVEL SECURITY;
CREATE POLICY model_pref_rows_tenant ON model_pref_rows USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE model_pref_cells ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_pref_cells FORCE ROW LEVEL SECURITY;
CREATE POLICY model_pref_cells_tenant ON model_pref_cells USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
