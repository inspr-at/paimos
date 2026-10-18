-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
CREATE TABLE harness_recoveries (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL,
 session_id uuid NOT NULL,
 requested_by uuid NOT NULL,
 action text NOT NULL CHECK(action IN ('restart','reconnect')),
 observed_revision text NOT NULL CHECK(length(observed_revision)=64),
 expected_ownership jsonb NOT NULL,
 request_digest bytea NOT NULL,
 context_digest bytea NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','claimed','completed','expired')),
 outcome text CHECK(outcome IN ('reconnected','continuation_queued','rejected','unconfirmed')),
 completion text CHECK(completion IN ('reconnected','exited','rejected')),
 next_run_id uuid,
 handover jsonb,
 service_tier text CHECK(service_tier IN ('default','fast','fastest')),
 display_label text CHECK(octet_length(display_label)<=2000),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '45 seconds',
 claimed_at timestamptz,
 completed_at timestamptz,
 PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,session_id) REFERENCES harness_sessions(tenant_id,id),
 FOREIGN KEY(tenant_id,requested_by) REFERENCES principals(tenant_id,id),
 FOREIGN KEY(tenant_id,next_run_id) REFERENCES agent_runs(tenant_id,id)
);
CREATE INDEX harness_recoveries_pending ON harness_recoveries(tenant_id,session_id,created_at) WHERE state IN ('pending','claimed');
CREATE UNIQUE INDEX harness_recoveries_one_live ON harness_recoveries(tenant_id,session_id) WHERE state IN ('pending','claimed');
CREATE UNIQUE INDEX harness_recoveries_one_continuation ON harness_recoveries(tenant_id,session_id) WHERE next_run_id IS NOT NULL;
CREATE UNIQUE INDEX harness_recoveries_continuation ON harness_recoveries(tenant_id,next_run_id) WHERE next_run_id IS NOT NULL;
ALTER TABLE harness_recoveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_recoveries FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_recoveries_tenant ON harness_recoveries USING (tenant_id = nullif(current_setting('aeon.tenant_id',true),'')::uuid) WITH CHECK (tenant_id = nullif(current_setting('aeon.tenant_id',true),'')::uuid);
