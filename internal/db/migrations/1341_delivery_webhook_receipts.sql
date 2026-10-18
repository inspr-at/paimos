-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1241 expansion: verified inbound deliveries survive downstream failure
-- and process restart. No existing writer or projection schema is changed.
SET LOCAL lock_timeout = '5s';

CREATE TABLE delivery_webhook_receipts (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 delivery_id text NOT NULL CHECK(length(delivery_id) BETWEEN 1 AND 128),
 repository text NOT NULL,
 installation_id text NOT NULL,
 event text NOT NULL,
 action text NOT NULL,
 payload_sha256 text NOT NULL,
 payload bytea NOT NULL CHECK(octet_length(payload) BETWEEN 1 AND 2097152),
 received_at timestamptz NOT NULL,
 checkpoints jsonb NOT NULL DEFAULT '{}',
 flow_runs jsonb NOT NULL DEFAULT '[]',
 attempts bigint NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL,
 last_step text,
 last_error_class text,
 finished_at timestamptz,
 PRIMARY KEY(tenant_id,delivery_id)
);
CREATE INDEX delivery_webhook_receipts_due ON delivery_webhook_receipts
 (tenant_id,next_attempt_at,delivery_id) WHERE finished_at IS NULL;

ALTER TABLE delivery_webhook_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_webhook_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_webhook_receipts_tenant ON delivery_webhook_receipts
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
