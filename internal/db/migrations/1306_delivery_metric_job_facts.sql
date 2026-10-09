-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1016 expansion: the Delivery page reads what Project Arion v5 steers by.
-- A run attempt carries the tree of its head commit (a retrigger commit has the
-- same tree) and, for attempt 1 of a pull-request or merge-queue run, what its
-- jobs said: the required checks' conclusions and the worst wait for a runner.
-- Review audits and escaped defects are reported facts of their own.
-- Nullable columns and a new table only; older writers leave the columns empty.
SET LOCAL lock_timeout = '5s';
ALTER TABLE delivery_metric_runs ADD COLUMN head_tree text;
ALTER TABLE delivery_metric_runs ADD COLUMN jobs_read_at timestamptz;
ALTER TABLE delivery_metric_runs ADD COLUMN jobs_failed_at timestamptz;
ALTER TABLE delivery_metric_runs ADD COLUMN worst_job_wait_ms integer;
ALTER TABLE delivery_metric_runs ADD COLUMN required_jobs jsonb;

-- Reported facts that are not timing marks: one row per audit or defect.
CREATE TABLE delivery_metric_reports (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 repository text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('review_audit','escaped_defect')),
 report_key text NOT NULL,
 pull_request bigint,
 release_name text,
 severity text NOT NULL CHECK(severity IN ('none','low','medium','high')),
 started_at timestamptz,
 at timestamptz NOT NULL,
 recorded_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,repository,kind,report_key)
);
CREATE INDEX delivery_metric_reports_window ON delivery_metric_reports(tenant_id,repository,at);
ALTER TABLE delivery_metric_reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_metric_reports FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_metric_reports_tenant ON delivery_metric_reports
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_metric_reports_project ON delivery_metric_reports AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR EXISTS(SELECT 1 FROM delivery_metric_sources s WHERE s.tenant_id=delivery_metric_reports.tenant_id AND s.repository=delivery_metric_reports.repository))
 WITH CHECK((SELECT aeon_visible_all()) OR EXISTS(SELECT 1 FROM delivery_metric_sources s WHERE s.tenant_id=delivery_metric_reports.tenant_id AND s.repository=delivery_metric_reports.repository));
