// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/jackc/pgx/v5"
)

// The read covers two of the longest windows (the window and the one just
// before it). Facts beyond these bounds are cut; coverage then starts later.
const (
	metricRunLimit  = 50000
	metricFactLimit = 10000
)

// Facts are idempotent upserts keyed by GitHub identity; replays, webhook
// redeliveries and repeated backfill pages converge on the same rows. The
// first source is kept, so coverage never moves later.
func upsertRunsTx(ctx context.Context, tx pgx.Tx, tid, repository, source string, runs []metricRun, at time.Time) error {
	for _, r := range runs {
		var tree *string
		if r.HeadTree != "" {
			tree = &r.HeadTree
		}
		// Job facts are stored with the run that read them; a later upsert of
		// the same attempt without them keeps what is there.
		var jobsAt *time.Time
		var required []byte
		if r.JobsRead {
			jobsAt = &at
			var err error
			if r.JobsComplete {
				if required, err = encodeStoredJobs(jobFacts{Conclusions: r.Required, Complete: true}); err != nil {
					return err
				}
			} else if required, err = json.Marshal(r.Required); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO delivery_metric_runs(tenant_id,repository,run_id,attempt,workflow_path,workflow_name,event,head_branch,head_sha,pull_request,created_at,started_at,completed_at,conclusion,source,recorded_at,head_tree,jobs_read_at,worst_job_wait_ms,required_jobs)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
			ON CONFLICT(tenant_id,repository,run_id,attempt) DO UPDATE SET workflow_path=EXCLUDED.workflow_path,workflow_name=EXCLUDED.workflow_name,event=EXCLUDED.event,head_branch=EXCLUDED.head_branch,head_sha=EXCLUDED.head_sha,
			pull_request=coalesce(EXCLUDED.pull_request,delivery_metric_runs.pull_request),created_at=EXCLUDED.created_at,started_at=EXCLUDED.started_at,completed_at=EXCLUDED.completed_at,conclusion=EXCLUDED.conclusion,
			head_tree=coalesce(EXCLUDED.head_tree,delivery_metric_runs.head_tree),
			jobs_read_at=coalesce(EXCLUDED.jobs_read_at,delivery_metric_runs.jobs_read_at),
			worst_job_wait_ms=CASE WHEN EXCLUDED.jobs_read_at IS NOT NULL THEN EXCLUDED.worst_job_wait_ms ELSE delivery_metric_runs.worst_job_wait_ms END,
			required_jobs=CASE WHEN EXCLUDED.jobs_read_at IS NOT NULL THEN EXCLUDED.required_jobs ELSE delivery_metric_runs.required_jobs END`,
			tid, repository, r.ID, r.Attempt, r.Workflow, r.Name, r.Event, r.Branch, r.Head, r.PR, r.Created, r.Started, r.Completed, r.Conclusion, source, at, tree, jobsAt, r.WorstWaitMS, required); err != nil {
			return err
		}
	}
	return nil
}

func upsertPullsTx(ctx context.Context, tx pgx.Tx, tid, repository, source string, pulls []metricPull, at time.Time) error {
	for _, p := range pulls {
		if _, err := tx.Exec(ctx, `INSERT INTO delivery_metric_pulls(tenant_id,repository,pull_request,head_branch,opened_at,merged_at,closed_at,source,recorded_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT(tenant_id,repository,pull_request) DO UPDATE SET head_branch=EXCLUDED.head_branch,opened_at=EXCLUDED.opened_at,
			merged_at=coalesce(delivery_metric_pulls.merged_at,EXCLUDED.merged_at),closed_at=EXCLUDED.closed_at`,
			tid, repository, p.Number, p.Branch, p.Opened, p.Merged, p.Closed, source, at); err != nil {
			return err
		}
	}
	return nil
}

// Webhook review statuses keep the earliest time per head and state;
// reported marks are corrected by a later report with the same key.
func upsertMarksTx(ctx context.Context, tx pgx.Tx, tid, repository, source string, marks []metricMark, at time.Time) error {
	for _, k := range marks {
		if _, err := tx.Exec(ctx, `INSERT INTO delivery_metric_marks(tenant_id,repository,kind,mark_key,pull_request,head_sha,started_at,at,outcome,source,recorded_at,release_name,release_tag,healthy_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			ON CONFLICT(tenant_id,repository,kind,mark_key) DO UPDATE SET
			pull_request=CASE WHEN EXCLUDED.source='report' THEN EXCLUDED.pull_request ELSE delivery_metric_marks.pull_request END,
			head_sha=CASE WHEN EXCLUDED.source='report' THEN EXCLUDED.head_sha ELSE delivery_metric_marks.head_sha END,
			started_at=CASE WHEN EXCLUDED.source='report' THEN EXCLUDED.started_at ELSE delivery_metric_marks.started_at END,
			at=CASE WHEN EXCLUDED.source='report' THEN EXCLUDED.at ELSE least(delivery_metric_marks.at,EXCLUDED.at) END,
			release_name=CASE WHEN EXCLUDED.source='report' THEN EXCLUDED.release_name ELSE delivery_metric_marks.release_name END,
			release_tag=CASE WHEN EXCLUDED.source='report' THEN EXCLUDED.release_tag ELSE delivery_metric_marks.release_tag END,
			healthy_at=CASE WHEN EXCLUDED.source='report' THEN EXCLUDED.healthy_at ELSE delivery_metric_marks.healthy_at END,
			outcome=EXCLUDED.outcome`,
			tid, repository, k.Kind, k.Key, k.PR, k.Head, k.Started, k.At, k.Outcome, source, at, k.Release, k.Tag, k.Healthy); err != nil {
			return err
		}
	}
	return nil
}

// upsertReportsTx stores reported audits and defects. A report with the same
// kind and key corrects the earlier one, so a ticket re-linked to another cause
// or re-rated keeps one row.
func upsertReportsTx(ctx context.Context, tx pgx.Tx, tid, repository string, reports []metricReport, at time.Time) error {
	for _, k := range reports {
		if _, err := tx.Exec(ctx, `INSERT INTO delivery_metric_reports(tenant_id,repository,kind,report_key,pull_request,release_name,severity,started_at,at,recorded_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT(tenant_id,repository,kind,report_key) DO UPDATE SET pull_request=EXCLUDED.pull_request,release_name=EXCLUDED.release_name,severity=EXCLUDED.severity,started_at=EXCLUDED.started_at,at=EXCLUDED.at`,
			tid, repository, k.Kind, k.Key, k.PR, k.Release, k.Severity, k.Started, k.At, at); err != nil {
			return err
		}
	}
	return nil
}

// metricsEvent records metric facts after the existing ingress authenticated,
// bound and accepted the delivery. Only content-free timing facts are kept.
func (m *Module) metricsEvent(ctx context.Context, name string, raw []byte) error {
	var e struct {
		Repository struct {
			Default string `json:"default_branch"`
		} `json:"repository"`
		Pull  githubPullFact `json:"pull_request"`
		Suite struct {
			ID     int64  `json:"id"`
			Head   string `json:"head_sha"`
			Status string `json:"status"`
			App    struct {
				Slug string `json:"slug"`
			} `json:"app"`
		} `json:"check_suite"`
		SHA     string    `json:"sha"`
		State   string    `json:"state"`
		Context string    `json:"context"`
		Created time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return errRead
	}
	var runs []metricRun
	var pulls []metricPull
	var marks []metricMark
	switch name {
	case "pull_request":
		// Only pulls into the default branch with an opened time are facts.
		if e.Repository.Default == "" || e.Pull.Base.Ref != e.Repository.Default || e.Pull.Created.IsZero() {
			return nil
		}
		pull, err := metricPullFrom(e.Pull, m.config.Repository)
		if err != nil {
			return err
		}
		pulls = []metricPull{pull}
	case "check_suite":
		reader, ok := m.github.(metricsReader)
		if !ok || e.Suite.App.Slug != "github-actions" || e.Suite.Status != "completed" {
			return nil
		}
		scope, err := m.jobScope(ctx)
		if err != nil {
			return err
		}
		err = reader.MetricsRead(ctx, m.config.TenantID, func(get func(string, any) error) error {
			var err error
			runs, err = suiteRuns(get, e.Suite.ID, e.Suite.Head)
			if err != nil {
				return err
			}
			// Job facts are best effort: a run stored without them is a visible
			// gap that the backfill's jobs pass fills, never a lost run.
			attachJobFacts(get, runs, scope.ci)
			return nil
		})
		if err != nil {
			return err
		}
	case "status":
		if e.Context != "aeon/review" {
			return nil
		}
		if !reviewgate.ValidSHA(e.SHA) || e.State != "pending" && e.State != "success" && e.State != "failure" && e.State != "error" {
			return errRead
		}
		at := e.Created.UTC()
		if e.Created.IsZero() {
			at = m.now()
		}
		marks = []metricMark{{Kind: "review_status", Key: e.SHA + "/" + e.State, Head: e.SHA, At: at, Outcome: e.State}}
	default:
		return nil
	}
	if len(runs)+len(pulls)+len(marks) == 0 {
		return nil
	}
	at := m.now()
	service := db.AllProjects(ctx, "delivery metric webhook facts")
	err := db.InTenant(service, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		if err := upsertRunsTx(ctx, tx, m.config.TenantID, m.config.Repository, "webhook", runs, at); err != nil {
			return err
		}
		if err := upsertPullsTx(ctx, tx, m.config.TenantID, m.config.Repository, "webhook", pulls, at); err != nil {
			return err
		}
		return upsertMarksTx(ctx, tx, m.config.TenantID, m.config.Repository, "webhook", marks, at)
	})
	if err != nil {
		return err
	}
	// AEON-1004: the same runs become checks and merge-queue steps in Flow.
	return m.flowFromRuns(ctx, runs)
}

// metricSourceRow is a project's repository link and backfill position.
type metricSourceRow struct {
	Project                                 string
	Repository, CIWorkflow, NightlyWorkflow string
	Cursor                                  *backfillCursor
	Revision                                int64
	Since, DoneAt                           *time.Time
}

func loadMetricSourceTx(ctx context.Context, tx pgx.Tx, project string, lock bool) (*metricSourceRow, error) {
	var s metricSourceRow
	var raw []byte
	query := `SELECT project_id::text,repository,ci_workflow,nightly_workflow,backfill_cursor,backfill_revision,backfill_since,backfill_done_at FROM delivery_metric_sources WHERE project_id=$1`
	if lock {
		query += ` FOR NO KEY UPDATE`
	}
	err := tx.QueryRow(ctx, query, project).Scan(&s.Project, &s.Repository, &s.CIWorkflow, &s.NightlyWorkflow, &raw, &s.Revision, &s.Since, &s.DoneAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if raw != nil {
		var c backfillCursor
		if err = json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		s.Cursor = &c
	}
	return &s, nil
}

// loadMetricInputTx reads the bounded window of facts, newest first: the
// longest window, the one before it and the slack. More facts than a bound
// cut the oldest; ReadFrom then names the time from which every fact was
// read (plus the slack that run and review pairs look back), so older days
// are partial, never silently complete.
func loadMetricInputTx(ctx context.Context, tx pgx.Tx, s metricSourceRow, now time.Time) (metricInput, error) {
	in := metricInput{CIWorkflow: s.CIWorkflow, NightlyWorkflow: s.NightlyWorkflow, Runs: []metricRun{}, Pulls: []metricPull{}, Marks: []metricMark{}}
	from := windowSpan(now, metricLongDays, true).first.Add(-metricSlack)
	cut := func(oldest time.Time) {
		in.ReadFrom = later(in.ReadFrom, oldest.UTC().Add(metricSlack))
	}
	rows, err := tx.Query(ctx, `SELECT run_id,attempt,workflow_path,workflow_name,event,head_branch,head_sha,pull_request,created_at,started_at,completed_at,conclusion,source,
		coalesce(head_tree,''),jobs_read_at IS NOT NULL,worst_job_wait_ms,required_jobs
		FROM delivery_metric_runs WHERE repository=$1 AND completed_at>=$2 ORDER BY completed_at DESC LIMIT $3`, s.Repository, from, metricRunLimit+1)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var r metricRun
		var required []byte
		if err = rows.Scan(&r.ID, &r.Attempt, &r.Workflow, &r.Name, &r.Event, &r.Branch, &r.Head, &r.PR, &r.Created, &r.Started, &r.Completed, &r.Conclusion, &r.Source,
			&r.HeadTree, &r.JobsRead, &r.WorstWaitMS, &required); err != nil {
			rows.Close()
			return in, err
		}
		if required != nil {
			conclusions, complete, err := decodeJobFacts(required)
			if err != nil {
				rows.Close()
				return in, err
			}
			r.Required, r.JobsComplete = conclusions, complete
		}
		in.Runs = append(in.Runs, r)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return in, err
	}
	if len(in.Runs) > metricRunLimit {
		in.Runs = in.Runs[:metricRunLimit]
		cut(in.Runs[metricRunLimit-1].Completed)
	}
	rows, err = tx.Query(ctx, `SELECT pull_request,head_branch,opened_at,merged_at,closed_at,source FROM delivery_metric_pulls
		WHERE repository=$1 AND merged_at>=$2 ORDER BY merged_at DESC LIMIT $3`, s.Repository, from, metricFactLimit+1)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var p metricPull
		if err = rows.Scan(&p.Number, &p.Branch, &p.Opened, &p.Merged, &p.Closed, &p.Source); err != nil {
			rows.Close()
			return in, err
		}
		in.Pulls = append(in.Pulls, p)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return in, err
	}
	if len(in.Pulls) > metricFactLimit {
		in.Pulls = in.Pulls[:metricFactLimit]
		cut(*in.Pulls[metricFactLimit-1].Merged)
	}
	rows, err = tx.Query(ctx, `SELECT kind,mark_key,pull_request,head_sha,started_at,at,outcome,source,release_name,release_tag,healthy_at FROM delivery_metric_marks
		WHERE repository=$1 AND at>=$2 ORDER BY at DESC LIMIT $3`, s.Repository, from, metricFactLimit+1)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var k metricMark
		if err = rows.Scan(&k.Kind, &k.Key, &k.PR, &k.Head, &k.Started, &k.At, &k.Outcome, &k.Source, &k.Release, &k.Tag, &k.Healthy); err != nil {
			rows.Close()
			return in, err
		}
		in.Marks = append(in.Marks, k)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return in, err
	}
	if len(in.Marks) > metricFactLimit {
		in.Marks = in.Marks[:metricFactLimit]
		cut(in.Marks[metricFactLimit-1].At)
	}
	rows, err = tx.Query(ctx, `SELECT kind,report_key,pull_request,release_name,severity,started_at,at FROM delivery_metric_reports
		WHERE repository=$1 AND at>=$2 ORDER BY at DESC LIMIT $3`, s.Repository, from, metricFactLimit+1)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var k metricReport
		if err = rows.Scan(&k.Kind, &k.Key, &k.PR, &k.Release, &k.Severity, &k.Started, &k.At); err != nil {
			rows.Close()
			return in, err
		}
		in.Reports = append(in.Reports, k)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return in, err
	}
	if len(in.Reports) > metricFactLimit {
		in.Reports = in.Reports[:metricFactLimit]
		cut(in.Reports[metricFactLimit-1].At)
	}
	// Coverage: a finished backfill covers from its start; otherwise facts are
	// complete from the first one the webhook recorded.
	if s.DoneAt != nil && s.Since != nil {
		v := s.Since.UTC()
		in.Covered = &v
	}
	var webhook *time.Time
	if err = tx.QueryRow(ctx, `SELECT min(recorded_at) FROM delivery_metric_runs WHERE repository=$1 AND source='webhook'`, s.Repository).Scan(&webhook); err != nil {
		return in, err
	}
	if webhook != nil && (in.Covered == nil || webhook.Before(*in.Covered)) {
		v := webhook.UTC()
		in.Covered = &v
	}
	// Preflight coverage is its own fact. CI being complete does not cover
	// ci-preflight.yml until a backfill has listed that workflow and finished
	// the runs pass. Until then the first webhook preflight fact is the start,
	// and a window that reaches earlier stays partial.
	if s.DoneAt != nil && s.Since != nil && s.Cursor != nil && s.Cursor.ResumePhase == "" &&
		(s.Cursor.Phase == "done" || s.Cursor.Phase == "jobs") && workflowListed(s.Cursor.Workflows, defaultPreflightWorkflow) {
		v := s.Since.UTC()
		in.PreflightCovered = &v
	} else {
		var first *time.Time
		if err = tx.QueryRow(ctx, `SELECT min(created_at) FROM delivery_metric_runs WHERE repository=$1 AND source='webhook' AND regexp_replace(workflow_path,'^.*/','')=$2`, s.Repository, path.Base(defaultPreflightWorkflow)).Scan(&first); err != nil {
			return in, err
		}
		if first != nil {
			v := first.UTC()
			in.PreflightCovered = &v
		}
	}
	if s.Cursor != nil && s.Cursor.Truncated {
		in.Truncated = true
	}
	return in, nil
}

// loadProjectInputTx adds what belongs to the project rather than the
// repository: its required checks and the rollout incidents of its releases.
// An incident is the production side of an escaped defect (down is high,
// degraded medium); coverage starts with the first release run recorded.
func loadProjectInputTx(ctx context.Context, tx pgx.Tx, project string, in *metricInput, now time.Time) error {
	settings, err := settingsTx(ctx, tx, &project)
	if err != nil {
		return err
	}
	if settings.RequiredChecks != nil {
		in.Required = append([]string(nil), (*settings.RequiredChecks)...)
	}
	from := windowSpan(now, metricLongDays, true).first.Add(-metricSlack)
	rows, err := tx.Query(ctx, `SELECT i.source_key,i.severity,i.started_at FROM delivery_flow_incidents i
		JOIN delivery_flow_items it ON it.tenant_id=i.tenant_id AND it.id=i.item_id
		WHERE i.project_id=$1 AND it.kind='release' AND i.started_at>=$2 ORDER BY i.started_at DESC LIMIT $3`, project, from, metricFactLimit+1)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key, severity string
		var at time.Time
		if err = rows.Scan(&key, &severity, &at); err != nil {
			rows.Close()
			return err
		}
		level := "medium"
		if severity == "down" {
			level = "high"
		}
		in.Incidents = append(in.Incidents, metricIncident{Key: key, Severity: level, At: at.UTC()})
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if len(in.Incidents) > metricFactLimit {
		in.Incidents = in.Incidents[:metricFactLimit]
		in.ReadFrom = later(in.ReadFrom, in.Incidents[metricFactLimit-1].At.Add(metricSlack))
	}
	var first *time.Time
	if err = tx.QueryRow(ctx, `SELECT min(started_at) FROM delivery_flow_items WHERE project_id=$1 AND kind='release'`, project).Scan(&first); err != nil {
		return err
	}
	if first != nil {
		v := first.UTC()
		in.IncidentCovered = &v
	}
	return nil
}

// jobScope is what the webhook needs to read jobs: whether a run belongs to the
// CI workflow of a project that reads this repository, and the required checks
// of those projects.
type jobScope struct {
	workflows []string
	Required  []string
}

func (j jobScope) ci(run metricRun) bool {
	for _, workflow := range j.workflows {
		if sameWorkflow(run, workflow) {
			return true
		}
	}
	return false
}

func (m *Module) jobScope(ctx context.Context) (jobScope, error) {
	var scope jobScope
	service := db.AllProjects(ctx, "delivery metric webhook job scope")
	err := db.InTenant(service, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT project_id::text,ci_workflow FROM delivery_metric_sources WHERE repository=$1 ORDER BY project_id`, m.config.Repository)
		if err != nil {
			return err
		}
		var projects []string
		for rows.Next() {
			var project, workflow string
			if err = rows.Scan(&project, &workflow); err != nil {
				rows.Close()
				return err
			}
			projects = append(projects, project)
			scope.workflows = append(scope.workflows, workflow)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		scope.Required, err = requiredChecksTx(ctx, tx, projects)
		return err
	})
	return scope, err
}

// requiredChecksTx is the union of the projects' required checks, in order.
// Without any project it is empty, and so is every job read.
func requiredChecksTx(ctx context.Context, tx pgx.Tx, projects []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, project := range projects {
		settings, err := settingsTx(ctx, tx, &project)
		if err != nil {
			return nil, err
		}
		if settings.RequiredChecks == nil {
			continue
		}
		for _, name := range *settings.RequiredChecks {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out, nil
}
