-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-314: a workspace's always-on budget for merged agent rules. The total
-- defaults to 12,000 bytes (ADR-004), the ceiling deployed clients enforce; it
-- may be lowered to 2,000 but never raised. Each layer may have its own cap
-- between 500 bytes and the total; NULL means no own cap. No row means the
-- defaults. The store caps (2,000 rules, 2 MiB of rule text) are separate and
-- unchanged.
SET LOCAL lock_timeout = '5s';
CREATE TABLE rule_budget_settings (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
    max_bytes integer NOT NULL DEFAULT 12000 CHECK (max_bytes BETWEEN 2000 AND 12000),
    company_bytes integer CHECK (company_bytes BETWEEN 500 AND 12000),
    project_bytes integer CHECK (project_bytes BETWEEN 500 AND 12000),
    person_bytes integer CHECK (person_bytes BETWEEN 500 AND 12000),
    agent_bytes integer CHECK (agent_bytes BETWEEN 500 AND 12000),
    updated_by uuid,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (coalesce(company_bytes, 0) <= max_bytes AND coalesce(project_bytes, 0) <= max_bytes
       AND coalesce(person_bytes, 0) <= max_bytes AND coalesce(agent_bytes, 0) <= max_bytes)
);
ALTER TABLE rule_budget_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE rule_budget_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON rule_budget_settings
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

