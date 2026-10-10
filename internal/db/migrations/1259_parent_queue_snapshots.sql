-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-740: snapshots grant queue membership only, never execution authority.
SET LOCAL lock_timeout = '5s';
CREATE TABLE parent_queue_snapshots (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 owner_id uuid NOT NULL,
 parent_id uuid NOT NULL,
 payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object' AND octet_length(payload::text)<=1048576),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,id),
 FOREIGN KEY (tenant_id,owner_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,parent_id) REFERENCES nodes(tenant_id,id)
);
ALTER TABLE parent_queue_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE parent_queue_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY parent_queue_snapshots_tenant ON parent_queue_snapshots
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE INDEX parent_queue_snapshots_owner ON parent_queue_snapshots(tenant_id,owner_id,created_at DESC);
