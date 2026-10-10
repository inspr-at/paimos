-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
-- Nullable additions preserve the released writers and immutable profile pins.
ALTER TABLE work_kinds ADD COLUMN examples text[], ADD COLUMN labels text[], ADD COLUMN label_override text;
ALTER TABLE model_profile_display ADD COLUMN introduced_at timestamptz;
CREATE TABLE model_pref_profiles (
 tenant_id uuid NOT NULL REFERENCES tenants(id), id uuid NOT NULL DEFAULT gen_random_uuid(),
 scope text NOT NULL CHECK (scope IN ('workspace','person')), person_id uuid,
 template text CHECK (template IN ('best','balanced','save')),
 thinking text CHECK (thinking IN ('lean','standard','deep','max')),
 usage text CHECK (usage IN ('careful','balanced','maxout')),
 residency text CHECK (residency IN ('any','eu','local')),
 hidden_kinds text[] NOT NULL DEFAULT '{}', dismissed_lines text[] NOT NULL DEFAULT '{}',
 revision bigint NOT NULL DEFAULT 1, set_by uuid, set_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id,id), FOREIGN KEY (tenant_id,person_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,set_by) REFERENCES principals(tenant_id,id),
 CHECK ((scope='workspace' AND person_id IS NULL) OR (scope='person' AND person_id IS NOT NULL))
);
CREATE UNIQUE INDEX model_pref_profiles_scope ON model_pref_profiles(tenant_id,scope,coalesce(person_id,'00000000-0000-0000-0000-000000000000'));
CREATE TABLE model_pref_orders (
 tenant_id uuid NOT NULL, profile_id uuid NOT NULL, column_key text NOT NULL,
 situation text NOT NULL CHECK (situation IN ('first','fix','stuck')),
 rank text[], not_allowed text[] NOT NULL DEFAULT '{}',
 thinking text CHECK (thinking IN ('lean','standard','deep','max')),
 set_by uuid, set_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY (tenant_id,profile_id,column_key,situation),
 FOREIGN KEY (tenant_id,profile_id) REFERENCES model_pref_profiles(tenant_id,id),
 FOREIGN KEY (tenant_id,set_by) REFERENCES principals(tenant_id,id)
);
CREATE TABLE model_rules (
 tenant_id uuid NOT NULL REFERENCES tenants(id), scope text NOT NULL CHECK (scope IN ('workspace','project')),
 project_id uuid, column_key text NOT NULL, line text NOT NULL,
 lock text NOT NULL CHECK (lock IN ('top','bottom','not')), position integer NOT NULL DEFAULT 0,
 why text NOT NULL, set_by uuid, set_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id), FOREIGN KEY (tenant_id,set_by) REFERENCES principals(tenant_id,id),
 CHECK ((scope='workspace' AND project_id IS NULL) OR (scope='project' AND project_id IS NOT NULL))
);
CREATE UNIQUE INDEX model_rules_scope ON model_rules(tenant_id,scope,coalesce(project_id,'00000000-0000-0000-0000-000000000000'),column_key,line);
CREATE TABLE model_rule_revisions (
 tenant_id uuid NOT NULL REFERENCES tenants(id), scope text NOT NULL, scope_key text NOT NULL,
 revision bigint NOT NULL DEFAULT 1, PRIMARY KEY (tenant_id,scope,scope_key)
);
CREATE TABLE model_situation_limits (
 tenant_id uuid PRIMARY KEY REFERENCES tenants(id), small_hours integer NOT NULL DEFAULT 2 CHECK (small_hours BETWEEN 1 AND 8),
 fix_rounds integer NOT NULL DEFAULT 3 CHECK (fix_rounds BETWEEN 1 AND 6), revision bigint NOT NULL DEFAULT 1,
 set_by uuid, set_at timestamptz NOT NULL DEFAULT now(), FOREIGN KEY (tenant_id,set_by) REFERENCES principals(tenant_id,id)
);
CREATE TABLE model_pref_migrations (
 tenant_id uuid PRIMARY KEY REFERENCES tenants(id), audit jsonb NOT NULL, migrated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE model_pref_profiles ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_pref_profiles FORCE ROW LEVEL SECURITY;
CREATE POLICY model_pref_profiles_tenant ON model_pref_profiles USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE model_pref_orders ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_pref_orders FORCE ROW LEVEL SECURITY;
CREATE POLICY model_pref_orders_tenant ON model_pref_orders USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE model_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_rules FORCE ROW LEVEL SECURITY;
CREATE POLICY model_rules_tenant ON model_rules USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE model_rule_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_rule_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY model_rule_revisions_tenant ON model_rule_revisions USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE model_situation_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_situation_limits FORCE ROW LEVEL SECURITY;
CREATE POLICY model_situation_limits_tenant ON model_situation_limits USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE model_pref_migrations ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_pref_migrations FORCE ROW LEVEL SECURITY;
CREATE POLICY model_pref_migrations_tenant ON model_pref_migrations USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

CREATE FUNCTION aeon_stamp_model_line_introduction() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.introduced_at := coalesce(NEW.introduced_at,now());
 RETURN NEW;
END;
$$;
CREATE TRIGGER model_line_introduction BEFORE INSERT ON model_profile_display FOR EACH ROW EXECUTE FUNCTION aeon_stamp_model_line_introduction();
