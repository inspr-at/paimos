-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1130 / S09 / M4: new accounting tables only. No existing writer,
-- subscription policy or default-off project acquires a new required field.
SET LOCAL lock_timeout = '5s';

CREATE TABLE routine_budget_balances (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 run_id uuid NOT NULL,
 owner_principal_id uuid NOT NULL,
 mode text NOT NULL CHECK (mode IN ('off','tokens','money','both')),
 token_ceiling bigint CHECK (token_ceiling>=0),
 money_ceiling_microusd bigint CHECK (money_ceiling_microusd>=0),
 recovery_ceiling_ms bigint NOT NULL CHECK (recovery_ceiling_ms>=0),
 settled_tokens bigint NOT NULL DEFAULT 0 CHECK (settled_tokens>=0),
 settled_microusd bigint NOT NULL DEFAULT 0 CHECK (settled_microusd>=0),
 settled_ms bigint NOT NULL DEFAULT 0 CHECK (settled_ms>=0),
 held_tokens bigint NOT NULL DEFAULT 0 CHECK (held_tokens>=0),
 held_microusd bigint NOT NULL DEFAULT 0 CHECK (held_microusd>=0),
 held_ms bigint NOT NULL DEFAULT 0 CHECK (held_ms>=0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,run_id),
 FOREIGN KEY (tenant_id,run_id) REFERENCES routine_runs(tenant_id,id),
 FOREIGN KEY (tenant_id,owner_principal_id) REFERENCES principals(tenant_id,id),
 CHECK ((mode IN ('tokens','both'))=(token_ceiling IS NOT NULL)),
 CHECK ((mode IN ('money','both'))=(money_ceiling_microusd IS NOT NULL)),
 CHECK (token_ceiling IS NULL OR settled_tokens::numeric+held_tokens<=token_ceiling),
 CHECK (money_ceiling_microusd IS NULL OR settled_microusd::numeric+held_microusd<=money_ceiling_microusd),
 CHECK (settled_ms::numeric+held_ms<=recovery_ceiling_ms)
);

CREATE TABLE routine_budget_grants (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 run_id uuid NOT NULL,
 owner_principal_id uuid NOT NULL,
 attempt_id uuid NOT NULL,
 action_id uuid NOT NULL,
 grant_key text NOT NULL CHECK (octet_length(grant_key) BETWEEN 1 AND 128),
 parent_id uuid,
 agent_run_id uuid NOT NULL,
 account_id uuid NOT NULL,
 harness text NOT NULL,
 billing_mode text NOT NULL,
 maximum_tokens bigint NOT NULL CHECK (maximum_tokens>=0),
 maximum_microusd bigint NOT NULL CHECK (maximum_microusd>=0),
 maximum_ms bigint NOT NULL CHECK (maximum_ms>=0),
 state text NOT NULL DEFAULT 'held' CHECK (state IN ('held','unknown','settled')),
 used_tokens bigint NOT NULL DEFAULT 0 CHECK (used_tokens>=0),
 used_microusd bigint NOT NULL DEFAULT 0 CHECK (used_microusd>=0),
 used_ms bigint NOT NULL DEFAULT 0 CHECK (used_ms>=0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 settled_at timestamptz,
 pending_receipt jsonb CHECK (pending_receipt IS NULL OR (jsonb_typeof(pending_receipt)='object' AND octet_length(pending_receipt::text)<=4096)),
 PRIMARY KEY (tenant_id,id),
 UNIQUE (tenant_id,run_id,id),
 UNIQUE (tenant_id,run_id,attempt_id,action_id,grant_key),
 FOREIGN KEY (tenant_id,run_id) REFERENCES routine_budget_balances(tenant_id,run_id),
 FOREIGN KEY (tenant_id,owner_principal_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,run_id,attempt_id) REFERENCES routine_attempts(tenant_id,run_id,id),
 FOREIGN KEY (tenant_id,run_id,action_id) REFERENCES routine_actions(tenant_id,run_id,id),
 FOREIGN KEY (tenant_id,run_id,parent_id) REFERENCES routine_budget_grants(tenant_id,run_id,id),
 FOREIGN KEY (tenant_id,agent_run_id) REFERENCES agent_runs(tenant_id,id),
 FOREIGN KEY (tenant_id,account_id) REFERENCES agent_accounts(tenant_id,id),
 CHECK (used_tokens<=maximum_tokens AND used_microusd<=maximum_microusd AND used_ms<=maximum_ms)
);
CREATE UNIQUE INDEX routine_budget_attempt_slot ON routine_budget_grants(tenant_id,run_id,attempt_id) WHERE parent_id IS NULL;
CREATE UNIQUE INDEX routine_budget_run_slot ON routine_budget_grants(tenant_id,agent_run_id) WHERE parent_id IS NULL;
CREATE INDEX routine_budget_owner_slots ON routine_budget_grants(tenant_id,owner_principal_id,harness) WHERE parent_id IS NULL AND state<>'settled';
CREATE INDEX routine_budget_children ON routine_budget_grants(tenant_id,parent_id,id) WHERE parent_id IS NOT NULL;

CREATE TABLE routine_budget_settlements (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 run_id uuid NOT NULL,
 grant_id uuid NOT NULL,
 event_key text NOT NULL CHECK (octet_length(event_key) BETWEEN 1 AND 128),
 receipt_digest text NOT NULL CHECK (receipt_digest ~ '^[a-f0-9]{64}$'),
 evidence_digest text NOT NULL CHECK (evidence_digest ~ '^[a-f0-9]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,grant_id),
 UNIQUE (tenant_id,run_id,event_key),
 FOREIGN KEY (tenant_id,run_id,grant_id) REFERENCES routine_budget_grants(tenant_id,run_id,id)
);

CREATE TABLE routine_budget_orders (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 run_id uuid NOT NULL,
 work_order_id uuid NOT NULL,
 PRIMARY KEY (tenant_id,work_order_id),
 FOREIGN KEY (tenant_id,run_id) REFERENCES routine_budget_balances(tenant_id,run_id),
 FOREIGN KEY (tenant_id,work_order_id) REFERENCES work_orders(tenant_id,node_id)
);

ALTER TABLE routine_budget_balances ENABLE ROW LEVEL SECURITY;
ALTER TABLE routine_budget_balances FORCE ROW LEVEL SECURITY;
CREATE POLICY routine_budget_balances_tenant ON routine_budget_balances
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY routine_budget_balances_scope ON routine_budget_balances AS RESTRICTIVE
 USING (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_budget_balances.tenant_id AND r.id=routine_budget_balances.run_id))
 WITH CHECK (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_budget_balances.tenant_id AND r.id=routine_budget_balances.run_id));
ALTER TABLE routine_budget_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE routine_budget_grants FORCE ROW LEVEL SECURITY;
CREATE POLICY routine_budget_grants_tenant ON routine_budget_grants
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY routine_budget_grants_scope ON routine_budget_grants AS RESTRICTIVE
 USING (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_budget_grants.tenant_id AND r.id=routine_budget_grants.run_id))
 WITH CHECK (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_budget_grants.tenant_id AND r.id=routine_budget_grants.run_id));
ALTER TABLE routine_budget_settlements ENABLE ROW LEVEL SECURITY;
ALTER TABLE routine_budget_settlements FORCE ROW LEVEL SECURITY;
CREATE POLICY routine_budget_settlements_tenant ON routine_budget_settlements
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY routine_budget_settlements_scope ON routine_budget_settlements AS RESTRICTIVE
 USING (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_budget_settlements.tenant_id AND r.id=routine_budget_settlements.run_id))
 WITH CHECK (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_budget_settlements.tenant_id AND r.id=routine_budget_settlements.run_id));
ALTER TABLE routine_budget_orders ENABLE ROW LEVEL SECURITY;
ALTER TABLE routine_budget_orders FORCE ROW LEVEL SECURITY;
CREATE POLICY routine_budget_orders_tenant ON routine_budget_orders
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY routine_budget_orders_scope ON routine_budget_orders AS RESTRICTIVE
 USING (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_budget_orders.tenant_id AND r.id=routine_budget_orders.run_id))
 WITH CHECK (EXISTS(SELECT 1 FROM routine_runs r WHERE r.tenant_id=routine_budget_orders.tenant_id AND r.id=routine_budget_orders.run_id));
