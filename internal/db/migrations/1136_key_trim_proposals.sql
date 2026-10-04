-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-615: no credential material is copied into approval records.
CREATE TABLE key_trim_proposals (
 tenant_id uuid NOT NULL REFERENCES tenants(id), id uuid NOT NULL DEFAULT gen_random_uuid(),
 key_id uuid NOT NULL, created_by uuid NOT NULL, request_id uuid NOT NULL, request_digest text NOT NULL,
 previous_scopes text[] NOT NULL CHECK(cardinality(previous_scopes)<=256), snapshot_digest text NOT NULL,
 candidate_scopes text[] NOT NULL CHECK(cardinality(candidate_scopes)<=256), candidate_digest text NOT NULL,
 evidence jsonb NOT NULL CHECK(octet_length(evidence::text)<=65536),
 usage jsonb NOT NULL CHECK(octet_length(usage::text)<=65536),
 created_at timestamptz NOT NULL, expires_at timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','applied','declined','restored')),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 decision_request_id uuid, decided_by uuid, decision text CHECK(decision IN ('approve','decline')),
 applied_at timestamptz, restore_until timestamptz,
 restore_request_id uuid, restored_by uuid,
 PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,created_by,request_id),
 FOREIGN KEY(tenant_id,key_id) REFERENCES agent_keys(tenant_id,id),
 FOREIGN KEY(tenant_id,created_by) REFERENCES principals(tenant_id,id),
 FOREIGN KEY(tenant_id,decided_by) REFERENCES principals(tenant_id,id),
 FOREIGN KEY(tenant_id,restored_by) REFERENCES principals(tenant_id,id),
 CHECK(expires_at>created_at AND expires_at<=created_at+interval '7 days'),
 CHECK(cardinality(candidate_scopes)<cardinality(previous_scopes)),
 CHECK(candidate_scopes <@ previous_scopes)
);
CREATE INDEX key_trim_proposals_page ON key_trim_proposals(tenant_id,state,id);
ALTER TABLE key_trim_proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE key_trim_proposals FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON key_trim_proposals
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
