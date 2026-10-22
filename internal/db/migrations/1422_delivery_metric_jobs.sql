-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1658 expansion: keep job queue/start times independently of the
-- workflow's completion. Old writers do not use this new table.
SET LOCAL lock_timeout = '5s';
CREATE TABLE delivery_metric_jobs (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 repository text NOT NULL,
 run_id bigint NOT NULL,
 attempt integer NOT NULL,
 job_id bigint NOT NULL,
 created_at timestamptz,
 started_at timestamptz,
 PRIMARY KEY(tenant_id,repository,run_id,attempt,job_id)
);
ALTER TABLE delivery_metric_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_metric_jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_metric_jobs_tenant ON delivery_metric_jobs
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_metric_jobs_project ON delivery_metric_jobs AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR EXISTS(SELECT 1 FROM delivery_metric_sources s WHERE s.tenant_id=delivery_metric_jobs.tenant_id AND s.repository=delivery_metric_jobs.repository))
 WITH CHECK((SELECT aeon_visible_all()) OR EXISTS(SELECT 1 FROM delivery_metric_sources s WHERE s.tenant_id=delivery_metric_jobs.tenant_id AND s.repository=delivery_metric_jobs.repository));
