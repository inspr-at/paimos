-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-319: references only. Draft text, App keys and tokens never enter SQL.
SET LOCAL lock_timeout = '5s';
CREATE TABLE doctrine_proposals (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL,
 source_id uuid NOT NULL,
 repository text NOT NULL CHECK(repository IN ('inspr-at/inspr-modules','inspr-at/inspr-doctrine-private')),
 path text NOT NULL,
 rule_key text NOT NULL,
 input_digest text NOT NULL CHECK(input_digest ~ '^[0-9a-f]{64}$'),
 base_commit text NOT NULL CHECK(base_commit ~ '^[0-9a-f]{40}$'),
 proposed_by uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 data jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(data)='object'),
 PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,proposed_by) REFERENCES principals(tenant_id,id)
);
ALTER TABLE doctrine_proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE doctrine_proposals FORCE ROW LEVEL SECURITY;
CREATE POLICY doctrine_proposals_tenant ON doctrine_proposals
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE TABLE doctrine_machine_pins (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 repository text NOT NULL CHECK(repository IN ('inspr-at/inspr-modules','inspr-at/inspr-doctrine-private')),
 machine_key text NOT NULL CHECK(machine_key ~ '^[0-9a-f]{64}$'),
 commit_sha text NOT NULL CHECK(commit_sha ~ '^[0-9a-f]{40}$'),
 reported_by uuid NOT NULL,
 reported_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,repository,machine_key),
 FOREIGN KEY(tenant_id,reported_by) REFERENCES principals(tenant_id,id)
);
ALTER TABLE doctrine_machine_pins ENABLE ROW LEVEL SECURITY;
ALTER TABLE doctrine_machine_pins FORCE ROW LEVEL SECURITY;
CREATE POLICY doctrine_machine_pins_tenant ON doctrine_machine_pins
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
