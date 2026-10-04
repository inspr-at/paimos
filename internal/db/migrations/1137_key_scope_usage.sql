-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-615: one timestamp per key/scope, no URL, payload, IP or request data.
CREATE TABLE agent_key_scope_usage (
 tenant_id uuid NOT NULL REFERENCES tenants(id), key_id uuid NOT NULL,
 scope text NOT NULL CHECK(octet_length(scope) BETWEEN 1 AND 128), last_used_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,key_id,scope),
 FOREIGN KEY(tenant_id,key_id) REFERENCES agent_keys(tenant_id,id)
);
ALTER TABLE agent_key_scope_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_key_scope_usage FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON agent_key_scope_usage
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
