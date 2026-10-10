-- SPDX-License-Identifier: AGPL-3.0-only
-- Expansion: no older binary writes queue failure rows. Workflow is nullable
-- so older settings writers remain compatible and project overrides can inherit.
SET LOCAL lock_timeout = '5s';
CREATE TABLE delivery_queue_failures (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 repository text NOT NULL,
 pull_request bigint NOT NULL,
 head_sha text NOT NULL,
 check_name text NOT NULL,
 workflow text NOT NULL,
 conclusion text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('required_failure','infra')),
 check_run_id bigint NOT NULL,
 at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,check_run_id)
);
CREATE INDEX delivery_queue_failure_head ON delivery_queue_failures(tenant_id,repository,pull_request,head_sha,at,check_run_id);
ALTER TABLE delivery_queue_failures ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_queue_failures FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_queue_failures_tenant ON delivery_queue_failures
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
-- Project visibility follows the current delivery item, including head history.
CREATE POLICY delivery_queue_failures_project ON delivery_queue_failures AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR EXISTS(SELECT 1 FROM delivery_items i
 WHERE i.tenant_id=delivery_queue_failures.tenant_id AND i.repository=delivery_queue_failures.repository
 AND i.pull_request=delivery_queue_failures.pull_request))
 WITH CHECK((SELECT aeon_visible_all()) OR EXISTS(SELECT 1 FROM delivery_items i
 WHERE i.tenant_id=delivery_queue_failures.tenant_id AND i.repository=delivery_queue_failures.repository
 AND i.pull_request=delivery_queue_failures.pull_request));
ALTER TABLE delivery_settings ADD COLUMN required_workflow text DEFAULT 'CI';
