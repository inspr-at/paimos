-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

-- A bounded, typed observation cache. Only server ingestion/settlement writes it;
-- account locks serialize changes, and original readings remain append-only.
CREATE TABLE account_capacity_learning (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 account_id uuid NOT NULL,
 state jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(state)='object'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id,account_id),
 FOREIGN KEY (tenant_id,account_id) REFERENCES agent_accounts(tenant_id,id)
);
ALTER TABLE account_capacity_learning ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_capacity_learning FORCE ROW LEVEL SECURITY;
CREATE POLICY account_capacity_learning_tenant ON account_capacity_learning
 USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

ALTER TABLE account_capacity_readings ADD COLUMN plus_minus numeric NOT NULL DEFAULT 0 CHECK (plus_minus BETWEEN 0 AND 100);
ALTER TABLE account_capacity_readings ADD COLUMN evidence jsonb CHECK (evidence IS NULL OR jsonb_typeof(evidence)='object');
CREATE INDEX capacity_learning_runs_idx ON agent_runs(tenant_id,account_id,started_at,ended_at);
