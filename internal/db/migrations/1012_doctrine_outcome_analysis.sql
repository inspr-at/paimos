-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-378: bounded, deterministic proposals. No instruction prose is stored.
SET LOCAL lock_timeout = '5s';
CREATE TABLE doctrine_analysis_runs (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 day date NOT NULL,
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 100),
 completed_at timestamptz,
 PRIMARY KEY (tenant_id, day)
);
CREATE TABLE doctrine_findings (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
 source_id uuid,
 path text NOT NULL DEFAULT '',
 rule_key text NOT NULL DEFAULT '',
 proposal_id uuid,
 status text NOT NULL CHECK (status IN ('pending','draft','internal_note','observed','closed')),
 data jsonb NOT NULL CHECK (jsonb_typeof(data)='object'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,id),
 UNIQUE (tenant_id,fingerprint),
 FOREIGN KEY (tenant_id,source_id) REFERENCES doctrine_sources(tenant_id,id)
);
CREATE UNIQUE INDEX doctrine_findings_one_open_rule ON doctrine_findings(tenant_id,source_id,path,rule_key)
 WHERE status IN ('pending','draft');
ALTER TABLE doctrine_analysis_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE doctrine_analysis_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY doctrine_analysis_runs_tenant ON doctrine_analysis_runs
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE doctrine_findings ENABLE ROW LEVEL SECURITY;
ALTER TABLE doctrine_findings FORCE ROW LEVEL SECURITY;
CREATE POLICY doctrine_findings_tenant ON doctrine_findings
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
