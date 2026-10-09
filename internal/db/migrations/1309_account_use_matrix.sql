-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

CREATE TABLE work_contexts (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 128),
    kind text NOT NULL DEFAULT 'regular' CHECK (kind IN ('default','regular','holding')),
    new_accounts_override text CHECK (new_accounts_override = 'deny'),
    archived_at timestamptz,
    revision bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (tenant_id,id),
    CHECK (kind='regular' OR archived_at IS NULL)
);
CREATE UNIQUE INDEX work_contexts_default ON work_contexts(tenant_id) WHERE kind='default';
CREATE UNIQUE INDEX work_contexts_holding ON work_contexts(tenant_id) WHERE kind='holding';
ALTER TABLE work_contexts ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_contexts FORCE ROW LEVEL SECURITY;
CREATE POLICY work_contexts_tenant ON work_contexts
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

CREATE TABLE project_work_contexts (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    project_id uuid NOT NULL,
    context_id uuid NOT NULL,
    PRIMARY KEY (tenant_id,project_id),
    FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id),
    FOREIGN KEY (tenant_id,context_id) REFERENCES work_contexts(tenant_id,id)
);
ALTER TABLE project_work_contexts ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_work_contexts FORCE ROW LEVEL SECURITY;
CREATE POLICY project_work_contexts_tenant ON project_work_contexts
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY project_work_contexts_visibility ON project_work_contexts AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
    WITH CHECK ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));

CREATE TABLE account_use_cells (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    account_id uuid NOT NULL,
    context_id uuid NOT NULL,
    source text NOT NULL CHECK (source IN ('person','rule','migration')),
    set_by uuid NOT NULL,
    set_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id,account_id,context_id),
    FOREIGN KEY (tenant_id,account_id) REFERENCES agent_accounts(tenant_id,id),
    FOREIGN KEY (tenant_id,context_id) REFERENCES work_contexts(tenant_id,id),
    FOREIGN KEY (tenant_id,set_by) REFERENCES principals(tenant_id,id)
);
ALTER TABLE account_use_cells ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_use_cells FORCE ROW LEVEL SECURITY;
CREATE POLICY account_use_cells_tenant ON account_use_cells
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

CREATE TABLE account_use_rules (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
    new_accounts text NOT NULL DEFAULT 'ask' CHECK (new_accounts IN ('allow','ask')),
    new_contexts text NOT NULL DEFAULT 'ask' CHECK (new_contexts IN ('allow','ask')),
    new_projects text NOT NULL DEFAULT 'default' CHECK (new_projects IN ('default','holding')),
    new_models text NOT NULL DEFAULT 'allow' CHECK (new_models IN ('allow','shipped_only','deny')),
    revision bigint NOT NULL DEFAULT 1,
    enforced_at timestamptz,
    confirmation_required boolean NOT NULL DEFAULT false,
    confirmed_at timestamptz,
    confirmed_by uuid,
    FOREIGN KEY (tenant_id,confirmed_by) REFERENCES principals(tenant_id,id)
);
ALTER TABLE account_use_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_use_rules FORCE ROW LEVEL SECURITY;
CREATE POLICY account_use_rules_tenant ON account_use_rules
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

-- Deployment-wide metadata, like schema_migrations; never stores tenant data.
CREATE TABLE aeon_required_capabilities (capability text PRIMARY KEY, since timestamptz NOT NULL DEFAULT now());
INSERT INTO aeon_required_capabilities(capability) VALUES ('account_use_v1');

CREATE FUNCTION aeon_account_use_gate() RETURNS boolean LANGUAGE plpgsql STABLE AS $$
BEGIN
    IF current_setting('aeon.account_use_capable',true) IS DISTINCT FROM 'on'
       AND EXISTS (SELECT 1 FROM account_use_rules WHERE enforced_at IS NOT NULL) THEN
        RAISE EXCEPTION USING ERRCODE='0A000', MESSAGE='account-use capability required: this binary is below the rollback floor';
    END IF;
    RETURN true;
END $$;

CREATE FUNCTION aeon_account_use_allowed_for_project(p_account uuid,p_project uuid) RETURNS boolean
LANGUAGE plpgsql STABLE AS $$
DECLARE target uuid; active boolean; prior text := current_setting('aeon.visible_projects',true);
BEGIN
    SELECT enforced_at IS NOT NULL INTO active FROM account_use_rules;
    IF active IS false THEN RETURN true; END IF;
    IF active IS NULL THEN RETURN false; END IF;
    IF p_project IS NULL THEN
        SELECT id INTO target FROM work_contexts WHERE kind='default' AND archived_at IS NULL;
    ELSE
        PERFORM set_config('aeon.visible_projects','*',true);
        SELECT m.context_id INTO target FROM project_work_contexts m
            JOIN work_contexts c ON c.tenant_id=m.tenant_id AND c.id=m.context_id
            WHERE m.project_id=p_project AND c.kind<>'holding' AND c.archived_at IS NULL;
        PERFORM set_config('aeon.visible_projects',coalesce(prior,''),true);
    END IF;
    RETURN target IS NOT NULL AND EXISTS (SELECT 1 FROM account_use_cells WHERE account_id=p_account AND context_id=target);
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('aeon.visible_projects',coalesce(prior,''),true);
    RAISE;
END $$;

CREATE FUNCTION aeon_account_use_allowed(p_account uuid,p_run uuid) RETURNS boolean
LANGUAGE plpgsql STABLE AS $$
DECLARE project uuid; node uuid; purpose text; prior text := current_setting('aeon.visible_projects',true);
BEGIN
    PERFORM set_config('aeon.visible_projects','*',true);
    SELECT coalesce(r.queue_node_id,r.work_order_id),r.purpose INTO node,purpose FROM agent_runs r WHERE r.id=p_run;
    IF NOT FOUND THEN
        PERFORM set_config('aeon.visible_projects',coalesce(prior,''),true);
        RETURN false;
    END IF;
    IF purpose='pairing_verification' AND EXISTS (
        SELECT 1 FROM agent_pairing_enrollments WHERE account_id=p_account AND verification_run_id=p_run
    ) THEN
        PERFORM set_config('aeon.visible_projects',coalesce(prior,''),true);
        RETURN true;
    END IF;
    IF node IS NOT NULL THEN
        SELECT n.project_id INTO project FROM nodes n WHERE n.id=node;
        IF NOT FOUND THEN
            PERFORM set_config('aeon.visible_projects',coalesce(prior,''),true);
            RETURN false;
        END IF;
    END IF;
    PERFORM set_config('aeon.visible_projects',coalesce(prior,''),true);
    RETURN aeon_account_use_allowed_for_project(p_account,project);
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('aeon.visible_projects',coalesce(prior,''),true);
    RAISE;
END $$;

CREATE FUNCTION aeon_account_use_reservation_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE account uuid;
BEGIN
    SELECT account_id INTO account FROM account_allowance_windows WHERE id=NEW.window_id;
    IF NOT aeon_account_use_allowed(account,NEW.run_id) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='account not allowed for work context';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER account_reservations_use_guard BEFORE INSERT ON account_reservations
    FOR EACH ROW EXECUTE FUNCTION aeon_account_use_reservation_guard();

CREATE FUNCTION aeon_account_use_start_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT aeon_account_use_allowed(NEW.account_id,NEW.id) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='account not allowed for work context';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER agent_runs_use_guard BEFORE UPDATE OF status ON agent_runs
    FOR EACH ROW WHEN (NEW.status='starting') EXECUTE FUNCTION aeon_account_use_start_guard();

CREATE FUNCTION aeon_account_use_cell_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM work_contexts WHERE id=NEW.context_id AND kind<>'holding' AND archived_at IS NULL) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='holding or archived context cannot be allowed';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER account_use_cells_guard BEFORE INSERT OR UPDATE ON account_use_cells
    FOR EACH ROW EXECUTE FUNCTION aeon_account_use_cell_guard();

-- No advisory locks in triggers. The Go writer owns the tenant/matrix fences.
CREATE FUNCTION aeon_account_use_activate() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE prior text := current_setting('aeon.visible_projects',true);
BEGIN
    IF current_setting('aeon.account_use_seeding',true)='on' THEN RETURN NULL; END IF;
    PERFORM set_config('aeon.visible_projects','*',true);
    UPDATE account_use_rules r SET enforced_at=clock_timestamp()
    WHERE r.enforced_at IS NULL AND (
        r.new_models='deny' OR r.new_projects='holding'
        OR EXISTS (SELECT 1 FROM project_work_contexts m JOIN work_contexts c
                   ON c.tenant_id=m.tenant_id AND c.id=m.context_id WHERE c.kind='holding' OR c.archived_at IS NOT NULL)
        OR EXISTS (SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
                   WHERE k.slug='project' AND n.deleted_at IS NULL
                   AND NOT EXISTS (SELECT 1 FROM project_work_contexts m WHERE m.project_id=n.id))
        OR EXISTS (SELECT 1 FROM agent_accounts a CROSS JOIN work_contexts c
                   WHERE a.archived_at IS NULL AND c.archived_at IS NULL AND c.kind<>'holding'
                   AND NOT EXISTS (SELECT 1 FROM account_use_cells x WHERE x.account_id=a.id AND x.context_id=c.id))
    );
    PERFORM set_config('aeon.visible_projects',coalesce(prior,''),true);
    RETURN NULL;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('aeon.visible_projects',coalesce(prior,''),true);
    RAISE;
END $$;
CREATE TRIGGER account_use_cells_activate AFTER INSERT OR UPDATE OR DELETE ON account_use_cells
    FOR EACH STATEMENT EXECUTE FUNCTION aeon_account_use_activate();
CREATE TRIGGER agent_accounts_use_activate AFTER INSERT ON agent_accounts
    FOR EACH STATEMENT EXECUTE FUNCTION aeon_account_use_activate();
CREATE TRIGGER work_contexts_use_activate AFTER INSERT OR UPDATE ON work_contexts
    FOR EACH STATEMENT EXECUTE FUNCTION aeon_account_use_activate();
CREATE TRIGGER project_work_contexts_activate AFTER INSERT OR UPDATE OR DELETE ON project_work_contexts
    FOR EACH STATEMENT EXECUTE FUNCTION aeon_account_use_activate();
CREATE TRIGGER account_use_rules_activate AFTER INSERT OR UPDATE OF new_models,new_projects ON account_use_rules
    FOR EACH STATEMENT EXECUTE FUNCTION aeon_account_use_activate();

CREATE FUNCTION aeon_account_use_rules_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.enforced_at IS NOT NULL AND NEW.enforced_at IS DISTINCT FROM OLD.enforced_at THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='account-use activation is monotonic';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER account_use_rules_guard BEFORE UPDATE ON account_use_rules
    FOR EACH ROW EXECUTE FUNCTION aeon_account_use_rules_guard();

CREATE FUNCTION aeon_account_use_context_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.kind<>'regular' AND (TG_OP='DELETE' OR NEW.kind IS DISTINCT FROM OLD.kind OR NEW.archived_at IS NOT NULL) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='default and holding contexts are permanent';
    END IF;
    RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;
CREATE TRIGGER work_contexts_use_guard BEFORE UPDATE OR DELETE ON work_contexts
    FOR EACH ROW EXECUTE FUNCTION aeon_account_use_context_guard();

CREATE FUNCTION aeon_account_use_stamp_context() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.kind='regular' AND NEW.archived_at IS NULL AND NEW.new_accounts_override IS NULL THEN
        INSERT INTO account_use_cells(tenant_id,account_id,context_id,source,set_by)
            SELECT NEW.tenant_id,a.id,NEW.id,'rule',p.id FROM agent_accounts a CROSS JOIN account_use_rules r
            JOIN principals p ON p.tenant_id=r.tenant_id AND p.kind='agent' AND p.name='System' AND p.roles @> ARRAY['system']::text[]
            WHERE r.new_contexts='allow' AND a.archived_at IS NULL;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER work_contexts_use_stamp AFTER INSERT ON work_contexts
    FOR EACH ROW EXECUTE FUNCTION aeon_account_use_stamp_context();

CREATE FUNCTION aeon_account_use_stamp_account() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO account_use_cells(tenant_id,account_id,context_id,source,set_by)
        SELECT NEW.tenant_id,NEW.id,c.id,'rule',NEW.registered_by_principal_id
        FROM work_contexts c CROSS JOIN account_use_rules r
        WHERE c.kind<>'holding' AND c.archived_at IS NULL AND c.new_accounts_override IS NULL AND r.new_accounts='allow';
    PERFORM set_config('aeon.account_use_account_'||replace(NEW.id::text,'-',''),
        (SELECT jsonb_build_object('account_id',NEW.id,'rule',new_accounts,'rule_revision',revision)::text FROM account_use_rules),true);
    RETURN NEW;
END $$;
CREATE TRIGGER agent_accounts_use_stamp AFTER INSERT ON agent_accounts
    FOR EACH ROW EXECUTE FUNCTION aeon_account_use_stamp_account();

CREATE FUNCTION aeon_account_use_account_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO events(tenant_id,actor_principal_id,type,after)
        VALUES(NEW.tenant_id,NEW.registered_by_principal_id,'account_use.changed',
               current_setting('aeon.account_use_account_'||replace(NEW.id::text,'-',''))::jsonb);
    RETURN NULL;
END $$;
-- Audit at transaction end: legacy writers may still lock additional rows.
CREATE CONSTRAINT TRIGGER agent_accounts_use_audit AFTER INSERT ON agent_accounts
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION aeon_account_use_account_audit();

CREATE FUNCTION aeon_account_use_stamp_project() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM node_kinds WHERE id=NEW.kind_id AND slug='project') THEN
        INSERT INTO project_work_contexts(tenant_id,project_id,context_id)
            SELECT NEW.tenant_id,NEW.id,c.id FROM work_contexts c CROSS JOIN account_use_rules r
            WHERE c.kind=r.new_projects;
        PERFORM set_config('aeon.account_use_project_'||replace(NEW.id::text,'-',''),
            (SELECT jsonb_build_object('project_id',NEW.id,'context_id',m.context_id,'rule',r.new_projects,'rule_revision',r.revision)::text
             FROM project_work_contexts m CROSS JOIN account_use_rules r WHERE m.project_id=NEW.id),true);
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER nodes_use_stamp AFTER INSERT ON nodes
    FOR EACH ROW EXECUTE FUNCTION aeon_account_use_stamp_project();

CREATE FUNCTION aeon_account_use_project_audit() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE actor uuid;
BEGIN
    IF EXISTS (SELECT 1 FROM node_kinds WHERE id=NEW.kind_id AND slug='project') THEN
        SELECT id INTO STRICT actor FROM principals WHERE tenant_id=NEW.tenant_id AND kind='agent' AND name='System' AND roles @> ARRAY['system']::text[];
        INSERT INTO events(tenant_id,actor_principal_id,type,node_id,after)
            VALUES(NEW.tenant_id,actor,'work_context.project_set',NEW.id,
                current_setting('aeon.account_use_project_'||replace(NEW.id::text,'-',''))::jsonb);
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER nodes_use_audit AFTER INSERT ON nodes
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION aeon_account_use_project_audit();

CREATE FUNCTION aeon_account_use_refresh_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE mirror boolean;
BEGIN
    SELECT new_models='allow' INTO mirror FROM account_use_rules;
    IF mirror IS NULL THEN
        RAISE EXCEPTION USING ERRCODE='42501', MESSAGE='account-use rules required';
    END IF;
    IF TG_OP='INSERT' THEN
        NEW.auto_add_profiles := mirror;
    ELSIF NEW.auto_add_profiles IS DISTINCT FROM OLD.auto_add_profiles THEN
        IF current_setting('aeon.account_use_rule_write',true) IS DISTINCT FROM 'on' THEN
            RAISE EXCEPTION USING ERRCODE='42501', MESSAGE='canonical account-use rule writer required';
        END IF;
        PERFORM set_config('aeon.account_use_rule_write','',true);
        IF NEW.auto_add_profiles IS DISTINCT FROM mirror THEN
            RAISE EXCEPTION USING ERRCODE='42501', MESSAGE='account-use model rule mirror mismatch';
        END IF;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER model_refresh_settings_use_guard BEFORE INSERT OR UPDATE ON model_refresh_settings
    FOR EACH ROW EXECUTE FUNCTION aeon_account_use_refresh_guard();

CREATE FUNCTION aeon_seed_account_use(p_tenant uuid,p_migrated boolean) RETURNS boolean
LANGUAGE plpgsql AS $$
DECLARE inserted integer; actor uuid; default_context uuid; prior text := current_setting('aeon.account_use_seeding',true);
BEGIN
    IF p_tenant IS DISTINCT FROM NULLIF(current_setting('aeon.tenant_id',true),'')::uuid THEN
        RAISE EXCEPTION 'tenant setting does not match account-use seed tenant';
    END IF;
    IF EXISTS (SELECT 1 FROM account_use_rules WHERE tenant_id=p_tenant) THEN RETURN false; END IF;
    INSERT INTO account_use_rules(tenant_id,new_accounts,new_contexts,new_models,confirmation_required)
        VALUES(p_tenant,CASE WHEN p_migrated THEN 'allow' ELSE 'ask' END,
               CASE WHEN p_migrated THEN 'allow' ELSE 'ask' END,
               CASE WHEN p_migrated AND EXISTS(SELECT 1 FROM model_refresh_settings WHERE NOT auto_add_profiles) THEN 'shipped_only' ELSE 'allow' END,
               p_migrated);
    GET DIAGNOSTICS inserted = ROW_COUNT;
    IF inserted=0 THEN RETURN false; END IF;
    PERFORM set_config('aeon.account_use_seeding','on',true);
    -- Prepare System before any resource or event-counter locks.
    SELECT id INTO actor FROM principals WHERE tenant_id=p_tenant AND kind='agent' AND name='System' AND roles @> ARRAY['system']::text[];
    IF actor IS NULL THEN
        INSERT INTO principals(tenant_id,kind,name,roles) VALUES(p_tenant,'agent','System',ARRAY['system']) RETURNING id INTO actor;
    END IF;
    INSERT INTO work_contexts(tenant_id,name,kind) VALUES(p_tenant,'Default','default') RETURNING id INTO default_context;
    INSERT INTO work_contexts(tenant_id,name,kind) VALUES(p_tenant,'Unassigned','holding');
    IF p_migrated THEN
        INSERT INTO account_use_cells(tenant_id,account_id,context_id,source,set_by)
            SELECT p_tenant,a.id,default_context,'migration',a.registered_by_principal_id FROM agent_accounts a WHERE a.archived_at IS NULL;
        INSERT INTO project_work_contexts(tenant_id,project_id,context_id)
            SELECT p_tenant,n.id,default_context FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='project';
        INSERT INTO events(tenant_id,actor_principal_id,type,after,metadata)
            VALUES(p_tenant,actor,'account_use.migrated',jsonb_build_object('confirmation_required',true),
                   jsonb_build_object('migration','1309','policy','preserve_pre_matrix_behaviour'));
    END IF;
    PERFORM set_config('aeon.account_use_seeding',coalesce(prior,''),true);
    RETURN true;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('aeon.account_use_seeding',coalesce(prior,''),true);
    RAISE;
END $$;

CREATE FUNCTION aeon_seed_new_tenant_account_use() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE prior text := current_setting('aeon.tenant_id',true);
BEGIN
    PERFORM set_config('aeon.tenant_id',NEW.id::text,true);
    PERFORM aeon_seed_account_use(NEW.id,false);
    PERFORM set_config('aeon.tenant_id',coalesce(prior,''),true);
    RETURN NEW;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('aeon.tenant_id',coalesce(prior,''),true);
    RAISE;
END $$;
CREATE TRIGGER tenants_seed_account_use AFTER INSERT ON tenants
    FOR EACH ROW EXECUTE FUNCTION aeon_seed_new_tenant_account_use();
