-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1614: Goals › Hide for me. One row hides one goal or release from one
-- person's Goals view; nobody else's view changes. Rows belong to their person:
-- reads and writes need the tenant and the row's principal in the caller's own
-- principal set, and that principal must be a person. An agent in the caller
-- set matches nothing. DSAR (aeon.system) may read and erase. A hard-deleted
-- goal drops its rows (cascade). Expansion only: a new table with no rows.
SET LOCAL lock_timeout = '5s';

CREATE TABLE hidden_goals (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    principal_id uuid NOT NULL,
    goal_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, principal_id, goal_id),
    FOREIGN KEY (tenant_id, principal_id) REFERENCES principals(tenant_id, id),
    FOREIGN KEY (tenant_id, goal_id) REFERENCES nodes(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX hidden_goals_goal ON hidden_goals (tenant_id, goal_id);

ALTER TABLE hidden_goals ENABLE ROW LEVEL SECURITY;
ALTER TABLE hidden_goals FORCE ROW LEVEL SECURITY;

-- The row's principal is the calling person; an agent in the caller set matches nothing.
CREATE FUNCTION hidden_goal_owned(p_tenant uuid, p_principal uuid) RETURNS boolean
LANGUAGE sql STABLE SET search_path = public, pg_temp AS $$
    SELECT p_principal = ANY (aeon_current_principals())
        AND EXISTS (
            SELECT 1 FROM principals p
            WHERE p.tenant_id = p_tenant AND p.id = p_principal AND p.kind = 'person')
        AND NOT EXISTS (
            SELECT 1 FROM principals a
            WHERE a.tenant_id = p_tenant AND a.kind = 'agent'
              AND a.id = ANY (aeon_current_principals()))
$$;

CREATE POLICY hidden_goals_tenant ON hidden_goals
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

CREATE POLICY hidden_goals_person_select ON hidden_goals AS RESTRICTIVE
    FOR SELECT
    USING ((SELECT hidden_goal_owned(tenant_id, principal_id)) OR (SELECT aeon_visibility_system()));

CREATE POLICY hidden_goals_person_insert ON hidden_goals AS RESTRICTIVE
    FOR INSERT
    WITH CHECK ((SELECT hidden_goal_owned(tenant_id, principal_id)));

CREATE POLICY hidden_goals_person_update ON hidden_goals AS RESTRICTIVE
    FOR UPDATE
    USING ((SELECT hidden_goal_owned(tenant_id, principal_id)))
    WITH CHECK ((SELECT hidden_goal_owned(tenant_id, principal_id)));

-- DELETE also lets the goal's cascade clear every person's rows once the goal
-- row is gone, as session_read_markers does for its sessions.
CREATE POLICY hidden_goals_person_delete ON hidden_goals AS RESTRICTIVE
    FOR DELETE
    USING (
        (SELECT hidden_goal_owned(tenant_id, principal_id))
        OR (SELECT aeon_visibility_system())
        OR (
            (SELECT aeon_visible_all())
            AND NOT EXISTS (
                SELECT 1 FROM nodes n
                WHERE n.tenant_id = hidden_goals.tenant_id
                  AND n.id = hidden_goals.goal_id)));
