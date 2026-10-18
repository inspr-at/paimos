-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-993 expansion: delivery metric facts per repository and the project
-- that reads them. New tables only; older binaries have no writers.
SET LOCAL lock_timeout = '5s';
CREATE TABLE delivery_metric_sources (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 repository text NOT NULL,
 ci_workflow text NOT NULL,
 nightly_workflow text NOT NULL,
 backfill_cursor jsonb,
 backfill_revision bigint NOT NULL DEFAULT 0,
 backfill_since timestamptz,
 backfill_done_at timestamptz,
 updated_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,project_id),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id)
);
CREATE INDEX delivery_metric_sources_repository ON delivery_metric_sources(tenant_id,repository,project_id);
ALTER TABLE delivery_metric_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_metric_sources FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_metric_sources_tenant ON delivery_metric_sources
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_metric_sources_project ON delivery_metric_sources AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));

-- One row per GitHub Actions workflow run attempt.
CREATE TABLE delivery_metric_runs (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 repository text NOT NULL,
 run_id bigint NOT NULL,
 attempt integer NOT NULL CHECK(attempt>0),
 workflow_path text NOT NULL,
 workflow_name text NOT NULL,
 event text NOT NULL,
 head_branch text NOT NULL,
 head_sha text NOT NULL,
 pull_request bigint,
 created_at timestamptz NOT NULL,
 started_at timestamptz NOT NULL,
 completed_at timestamptz NOT NULL,
 conclusion text NOT NULL,
 source text NOT NULL CHECK(source IN ('webhook','backfill')),
 recorded_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,repository,run_id,attempt)
);
CREATE INDEX delivery_metric_runs_window ON delivery_metric_runs(tenant_id,repository,completed_at);
CREATE INDEX delivery_metric_runs_source ON delivery_metric_runs(tenant_id,repository,source,recorded_at);

-- One row per pull request.
CREATE TABLE delivery_metric_pulls (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 repository text NOT NULL,
 pull_request bigint NOT NULL,
 head_branch text NOT NULL,
 opened_at timestamptz NOT NULL,
 merged_at timestamptz,
 closed_at timestamptz,
 source text NOT NULL CHECK(source IN ('webhook','backfill')),
 recorded_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,repository,pull_request)
);
CREATE INDEX delivery_metric_pulls_merged ON delivery_metric_pulls(tenant_id,repository,merged_at);

-- Review statuses from the webhook and reported reviews, merge rounds and releases.
CREATE TABLE delivery_metric_marks (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 repository text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('review_status','review','merge_round','release')),
 mark_key text NOT NULL,
 pull_request bigint,
 head_sha text NOT NULL DEFAULT '',
 started_at timestamptz,
 at timestamptz NOT NULL,
 outcome text NOT NULL,
 source text NOT NULL CHECK(source IN ('webhook','report')),
 recorded_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,repository,kind,mark_key)
);
CREATE INDEX delivery_metric_marks_window ON delivery_metric_marks(tenant_id,repository,at);

-- Repository facts are visible through a visible project that reads them.
ALTER TABLE delivery_metric_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_metric_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_metric_runs_tenant ON delivery_metric_runs
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_metric_runs_project ON delivery_metric_runs AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR EXISTS(SELECT 1 FROM delivery_metric_sources s WHERE s.tenant_id=delivery_metric_runs.tenant_id AND s.repository=delivery_metric_runs.repository))
 WITH CHECK((SELECT aeon_visible_all()) OR EXISTS(SELECT 1 FROM delivery_metric_sources s WHERE s.tenant_id=delivery_metric_runs.tenant_id AND s.repository=delivery_metric_runs.repository));
ALTER TABLE delivery_metric_pulls ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_metric_pulls FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_metric_pulls_tenant ON delivery_metric_pulls
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_metric_pulls_project ON delivery_metric_pulls AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR EXISTS(SELECT 1 FROM delivery_metric_sources s WHERE s.tenant_id=delivery_metric_pulls.tenant_id AND s.repository=delivery_metric_pulls.repository))
 WITH CHECK((SELECT aeon_visible_all()) OR EXISTS(SELECT 1 FROM delivery_metric_sources s WHERE s.tenant_id=delivery_metric_pulls.tenant_id AND s.repository=delivery_metric_pulls.repository));
ALTER TABLE delivery_metric_marks ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_metric_marks FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_metric_marks_tenant ON delivery_metric_marks
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_metric_marks_project ON delivery_metric_marks AS RESTRICTIVE
 USING((SELECT aeon_visible_all()) OR EXISTS(SELECT 1 FROM delivery_metric_sources s WHERE s.tenant_id=delivery_metric_marks.tenant_id AND s.repository=delivery_metric_marks.repository))
 WITH CHECK((SELECT aeon_visible_all()) OR EXISTS(SELECT 1 FROM delivery_metric_sources s WHERE s.tenant_id=delivery_metric_marks.tenant_id AND s.repository=delivery_metric_marks.repository));
