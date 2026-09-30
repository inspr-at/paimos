-- SPDX-License-Identifier: AGPL-3.0-only
-- Account groups (AEON-389): one vendor, named members, an optional project
-- fence, and an exclusive flag. Membership is agent_accounts.group_id.
SET LOCAL lock_timeout = '5s';

CREATE TABLE account_groups (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    harness text NOT NULL CHECK (harness IN ('codex', 'claude', 'pi', 'cursor', 'grok')),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80 AND name = btrim(name)),
    exclusive boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, harness, name)
);
ALTER TABLE account_groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_groups FORCE ROW LEVEL SECURITY;
CREATE POLICY account_groups_tenant ON account_groups
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

CREATE TABLE account_group_projects (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    group_id uuid NOT NULL,
    project_id uuid NOT NULL,
    PRIMARY KEY (tenant_id, group_id, project_id),
    FOREIGN KEY (tenant_id, group_id) REFERENCES account_groups(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, project_id) REFERENCES nodes(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX account_group_projects_project_idx ON account_group_projects(tenant_id, project_id);
ALTER TABLE account_group_projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_group_projects FORCE ROW LEVEL SECURITY;
CREATE POLICY account_group_projects_tenant ON account_group_projects
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

ALTER TABLE agent_accounts ADD COLUMN group_id uuid;
ALTER TABLE agent_accounts ADD CONSTRAINT agent_accounts_group_fk
    FOREIGN KEY (tenant_id, group_id) REFERENCES account_groups(tenant_id, id);
CREATE INDEX agent_accounts_group_idx ON agent_accounts(tenant_id, group_id) WHERE group_id IS NOT NULL;

CREATE FUNCTION aeon_account_group_member() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.group_id IS NULL THEN
        RETURN NEW;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM account_groups g
        WHERE g.tenant_id = NEW.tenant_id AND g.id = NEW.group_id AND g.harness = NEW.harness
    ) THEN
        RAISE EXCEPTION 'group harness mismatch' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER agent_accounts_group_member
    BEFORE INSERT OR UPDATE OF group_id, harness ON agent_accounts
    FOR EACH ROW EXECUTE FUNCTION aeon_account_group_member();

CREATE FUNCTION aeon_account_group_project() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    group_harness text;
BEGIN
    SELECT g.harness INTO group_harness FROM account_groups g
    WHERE g.tenant_id = NEW.tenant_id AND g.id = NEW.group_id;
    IF group_harness IS NULL THEN
        RAISE EXCEPTION 'invalid group' USING ERRCODE = '23503';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM nodes n
        JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
        WHERE n.tenant_id = NEW.tenant_id AND n.id = NEW.project_id
          AND k.slug = 'project' AND n.deleted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'group project must be a project' USING ERRCODE = '23514';
    END IF;
    IF EXISTS (
        SELECT 1 FROM account_group_projects gp
        JOIN account_groups g ON g.tenant_id = gp.tenant_id AND g.id = gp.group_id
        WHERE gp.tenant_id = NEW.tenant_id AND gp.project_id = NEW.project_id
          AND g.harness = group_harness AND gp.group_id <> NEW.group_id
    ) THEN
        RAISE EXCEPTION 'project already belongs to a group for this harness' USING ERRCODE = '23505';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER account_group_projects_guard
    BEFORE INSERT OR UPDATE ON account_group_projects
    FOR EACH ROW EXECUTE FUNCTION aeon_account_group_project();

-- Exclusive is checked at commit so a group and its projects can be written together.
CREATE FUNCTION aeon_account_group_exclusive() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    tid uuid;
    gid uuid;
    excl boolean;
BEGIN
    IF TG_TABLE_NAME = 'account_groups' THEN
        tid := NEW.tenant_id;
        gid := NEW.id;
        excl := NEW.exclusive;
    ELSE
        tid := COALESCE(NEW.tenant_id, OLD.tenant_id);
        gid := COALESCE(NEW.group_id, OLD.group_id);
        SELECT exclusive INTO excl FROM account_groups WHERE tenant_id = tid AND id = gid;
    END IF;
    IF excl AND NOT EXISTS (
        SELECT 1 FROM account_group_projects WHERE tenant_id = tid AND group_id = gid
    ) THEN
        RAISE EXCEPTION 'exclusive group needs a project' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER account_groups_exclusive_guard
    AFTER INSERT OR UPDATE OF exclusive ON account_groups
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION aeon_account_group_exclusive();
CREATE CONSTRAINT TRIGGER account_group_projects_exclusive_guard
    AFTER INSERT OR UPDATE OR DELETE ON account_group_projects
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION aeon_account_group_exclusive();

-- Clearing members keeps tenant_id; a composite ON DELETE SET NULL would not.
CREATE FUNCTION aeon_account_group_delete() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE agent_accounts SET group_id = NULL WHERE tenant_id = OLD.tenant_id AND group_id = OLD.id;
    DELETE FROM account_capacity_schedules
    WHERE tenant_id = OLD.tenant_id AND scope = 'group' AND scope_key = OLD.id::text;
    RETURN OLD;
END;
$$;
CREATE TRIGGER account_groups_delete
    BEFORE DELETE ON account_groups
    FOR EACH ROW EXECUTE FUNCTION aeon_account_group_delete();
