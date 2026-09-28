-- SPDX-License-Identifier: AGPL-3.0-only
-- Content-free approval and lease only. No column may store conversation text.
CREATE TABLE harness_attach_requests (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL,
 computer_id uuid NOT NULL,
 owner_id uuid NOT NULL,
 project_id uuid NOT NULL,
 ticket_id uuid NOT NULL,
 snapshot jsonb NOT NULL,
 digest text NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
 user_code text NOT NULL CHECK (user_code ~ '^[0-9]{9}$'),
 state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','approved','active','detached','unreachable')),
 expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '10 minutes',
 lease_until timestamptz,
 session_id uuid,
 sequence bigint NOT NULL DEFAULT 0,
 last_poll timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,id),
 UNIQUE(tenant_id,user_code),
 UNIQUE(tenant_id,session_id),
 FOREIGN KEY(tenant_id,computer_id) REFERENCES agent_pairing_computers(tenant_id,id),
 FOREIGN KEY(tenant_id,owner_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY(tenant_id,ticket_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY(tenant_id,session_id) REFERENCES harness_sessions(tenant_id,id)
);
CREATE INDEX harness_attach_computer ON harness_attach_requests(tenant_id,computer_id,state);
ALTER TABLE harness_attach_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_attach_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_attach_tenant ON harness_attach_requests
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
-- Fixed tenant buckets bound guessing and request flooding across processes.
CREATE TABLE harness_attach_limits (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 bucket text NOT NULL CHECK(bucket IN ('request','lookup')),
 starts_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 attempts integer NOT NULL DEFAULT 0,
 PRIMARY KEY(tenant_id,bucket)
);
ALTER TABLE harness_attach_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_attach_limits FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_attach_limits_tenant ON harness_attach_limits
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
