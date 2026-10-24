-- SPDX-License-Identifier: AGPL-3.0-only
-- Expand-only: new policy-owned ledgers and nullable bindings. Old inbox and
-- routine writers need no new fields or privileges.
SET LOCAL lock_timeout = '5s';

CREATE TABLE assistant_budget_reservations (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    principal_id uuid NOT NULL,
    operation_key text NOT NULL,
    week_start timestamptz NOT NULL,
    unit text NOT NULL,
    amount_micro bigint NOT NULL,
    unknown_cost boolean NOT NULL,
    policy_revision bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, principal_id, operation_key),
    FOREIGN KEY (tenant_id, principal_id) REFERENCES principals(tenant_id, id)
);
CREATE INDEX assistant_budget_week ON assistant_budget_reservations(tenant_id,principal_id,week_start,unit);
ALTER TABLE assistant_budget_reservations ENABLE ROW LEVEL SECURITY;
ALTER TABLE assistant_budget_reservations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON assistant_budget_reservations
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

CREATE TABLE assistant_policy_receipts (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    principal_id uuid NOT NULL,
    project_id uuid,
    operation_key text NOT NULL,
    operation text NOT NULL,
    policy_revision bigint NOT NULL,
    allowed boolean NOT NULL,
    reason text NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id,id),
    FOREIGN KEY (tenant_id,principal_id) REFERENCES principals(tenant_id,id),
    FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id),
    UNIQUE (tenant_id,principal_id,operation_key,operation,policy_revision,allowed,reason)
);
CREATE INDEX assistant_policy_activity ON assistant_policy_receipts(tenant_id,principal_id,recorded_at DESC,id DESC);
ALTER TABLE assistant_policy_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE assistant_policy_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON assistant_policy_receipts
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY project_visibility ON assistant_policy_receipts AS RESTRICTIVE
 USING (project_id IS NULL OR (SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK (project_id IS NULL OR (SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));

ALTER TABLE inbox_messages ADD COLUMN assistant_policy_key text;
ALTER TABLE inbox_messages ADD COLUMN assistant_policy_project_id uuid;
ALTER TABLE inbox_message_deliveries ADD COLUMN assistant_policy_reason text;
ALTER TABLE routine_runs ADD COLUMN agent_delegation jsonb;
