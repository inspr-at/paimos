-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1047: native one-change requests; no agent grant or key changes.
SET LOCAL lock_timeout = '5s';
CREATE TABLE stepup_requests (
 tenant_id uuid NOT NULL REFERENCES tenants(id), id uuid NOT NULL DEFAULT gen_random_uuid(),
 requested_by uuid NOT NULL, session_id uuid, project_id uuid,
 permission text NOT NULL, payload jsonb NOT NULL CHECK(octet_length(payload::text)<=32768),
 before_value jsonb NOT NULL CHECK(octet_length(before_value::text)<=32768),
 after_value jsonb NOT NULL CHECK(octet_length(after_value::text)<=32768),
 before_hash text NOT NULL, after_hash text NOT NULL, request_digest text NOT NULL,
 created_at timestamptz NOT NULL, expires_at timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','applied','declined','expired','stale','withdrawn','failed')),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 decided_by uuid, decision text CHECK(decision IN ('approve','decline','withdraw','expire')),
 method text CHECK(method IN ('passkey_platform','passkey','oidc_reauth')),
 auth_time timestamptz, applied_at timestamptz, decided_at timestamptz,
 PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,requested_by) REFERENCES principals(tenant_id,id),
 FOREIGN KEY(tenant_id,decided_by) REFERENCES principals(tenant_id,id),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 CHECK(expires_at>created_at AND expires_at<=created_at+interval '15 minutes')
);
CREATE INDEX stepup_pending_page ON stepup_requests(tenant_id,state,expires_at,id);
CREATE INDEX stepup_decided_page ON stepup_requests(tenant_id,decided_at DESC,id DESC) WHERE state<>'pending';
ALTER TABLE stepup_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE stepup_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY stepup_tenant ON stepup_requests
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

-- The only replaced constraints are the two 1092 kind checks. These are strict
-- supersets: every old writer value stays valid. No rows or authority change.
ALTER TABLE phone_approval_challenges ADD CONSTRAINT phone_approval_challenges_kind_stepup_check
 CHECK(kind IN ('registration','approval','attach','stepup')) NOT VALID;
ALTER TABLE phone_approval_challenges VALIDATE CONSTRAINT phone_approval_challenges_kind_stepup_check;
ALTER TABLE phone_approval_challenges DROP CONSTRAINT phone_approval_challenges_kind_check;
ALTER TABLE phone_push_deliveries ADD CONSTRAINT phone_push_deliveries_kind_stepup_check
 CHECK(kind IN ('approval','attach','stepup')) NOT VALID;
ALTER TABLE phone_push_deliveries VALIDATE CONSTRAINT phone_push_deliveries_kind_stepup_check;
ALTER TABLE phone_push_deliveries DROP CONSTRAINT phone_push_deliveries_kind_check;
