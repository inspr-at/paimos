// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type MetricSource struct {
	Repository      string     `json:"repository"`
	CIWorkflow      string     `json:"ci_workflow"`
	NightlyWorkflow string     `json:"nightly_workflow"`
	AppConnected    bool       `json:"app_connected"`
	Backfill        string     `json:"backfill"`
	BackfillSince   *time.Time `json:"backfill_since"`
	BackfillDoneAt  *time.Time `json:"backfill_done_at"`
	CoveredSince    *time.Time `json:"covered_since"`
}

type DeliveryMetrics struct {
	ProjectID   string        `json:"project_id"`
	GeneratedAt time.Time     `json:"generated_at"`
	Source      *MetricSource `json:"source"`
	Metrics     []Metric      `json:"metrics"`
}

type metricSourceInput struct {
	Repository      string  `json:"repository"`
	CIWorkflow      *string `json:"ci_workflow"`
	NightlyWorkflow *string `json:"nightly_workflow"`
}

type backfillInput struct {
	Days    *int `json:"days"`
	Restart bool `json:"restart"`
}

type BackfillResult struct {
	State string    `json:"state"`
	Since time.Time `json:"since"`
	Phase string    `json:"phase"`
	Day   *string   `json:"day"`
	Calls int       `json:"calls"`
	Runs  int       `json:"runs"`
	Pulls int       `json:"pulls"`
	// Jobs counts the runs whose jobs this step read (the jobs pass that follows
	// the runs and pulls phases).
	Jobs      int  `json:"jobs"`
	Truncated bool `json:"truncated"`
}

type metricFactInput struct {
	Kind        string     `json:"kind"`
	Key         string     `json:"key"`
	PullRequest *int64     `json:"pull_request"`
	HeadSHA     *string    `json:"head_sha"`
	StartedAt   *time.Time `json:"started_at"`
	At          time.Time  `json:"at"`
	Outcome     string     `json:"outcome"`
	Release     *string    `json:"release"`
	Tag         *string    `json:"tag"`
	HealthyAt   *time.Time `json:"healthy_at"`
}

type metricFactsInput struct {
	Facts []metricFactInput `json:"facts"`
}

var (
	metricFactKey     = regexp.MustCompile(`^[A-Za-z0-9._:/+-]{1,128}$`)
	metricReleaseName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
	metricReleaseTag  = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,128}$`)
)

const (
	defaultCIWorkflow      = ".github/workflows/ci.yml"
	defaultNightlyWorkflow = ".github/workflows/nightly-full.yml"
)

func (m *Module) mountMetrics(mux *http.ServeMux) {
	mount := func(pattern string, timeout time.Duration, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			if r.Method != http.MethodGet {
				controller := http.NewResponseController(w)
				_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
				defer controller.SetReadDeadline(time.Time{})
			}
			handler(w, r.WithContext(ctx))
		})
	}
	mount("GET /api/projects/{projectId}/delivery/metrics", 15*time.Second, m.getMetrics)
	mount("PUT /api/projects/{projectId}/delivery/metrics/source", 10*time.Second, m.putMetricSource)
	mount("POST /api/projects/{projectId}/delivery/metrics/backfill", 2*time.Minute, m.backfillMetrics)
	mount("POST /api/projects/{projectId}/delivery/metrics/facts", 10*time.Second, m.reportMetricFacts)
}

func decodeBounded(w http.ResponseWriter, r *http.Request, limit int64, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fail(400, "invalid delivery metrics JSON")
	}
	if dec.Decode(new(any)) != io.EOF {
		return fail(400, "trailing delivery metrics JSON")
	}
	return nil
}

// metricProject resolves the path project. An invisible project is absent.
func metricProject(w http.ResponseWriter, r *http.Request) (tenant.Principal, string, bool) {
	p, ok := principal(w, r)
	if !ok {
		return p, "", false
	}
	project := r.PathValue("projectId")
	if !workorders.UUID(project) {
		respondError(w, fail(400, "invalid metrics project"))
		return p, "", false
	}
	return p, project, true
}

// requireMetrics authorizes inside the transaction that reads or writes.
func requireMetrics(ctx context.Context, tx pgx.Tx, p tenant.Principal, permission, project string) error {
	return authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project})
}

func (m *Module) appConnected(tenantID, repository string) bool {
	app := crossreview.GitHubApp{Config: m.config}
	_, reads := m.github.(metricsReader)
	return reads && app.Configured(tenantID, repository)
}

func appendMetricReason(m *Metric, note string) {
	if m.Status == "ok" {
		return
	}
	if m.Reason == nil {
		m.Reason = &note
		return
	}
	if strings.Contains(*m.Reason, note) {
		return
	}
	joined := *m.Reason + " " + note
	m.Reason = &joined
}

// githubCoverageEnd is the last instant GitHub facts are known to be complete
// once the App is disconnected. It is always before now, so an open window
// cannot stay "ok" across the unobserved interval.
func githubCoverageEnd(s metricSourceRow, in metricInput, now time.Time) time.Time {
	var end *time.Time
	if s.DoneAt != nil {
		v := s.DoneAt.UTC()
		end = &v
	}
	for _, run := range in.Runs {
		end = later(end, run.Completed)
	}
	for _, pull := range in.Pulls {
		if pull.Merged != nil {
			end = later(end, pull.Merged.UTC())
		}
	}
	for _, mark := range in.Marks {
		if mark.Source != "report" {
			end = later(end, mark.At)
		}
	}
	if end != nil && end.Before(now) {
		return end.UTC()
	}
	return now.Add(-time.Nanosecond)
}

func (m *Module) sourceView(tenantID string, s metricSourceRow, covered *time.Time) *MetricSource {
	out := &MetricSource{Repository: s.Repository, CIWorkflow: s.CIWorkflow, NightlyWorkflow: s.NightlyWorkflow, AppConnected: m.appConnected(tenantID, s.Repository), Backfill: "not_started", BackfillSince: s.Since, BackfillDoneAt: s.DoneAt, CoveredSince: covered}
	if s.DoneAt != nil {
		out.Backfill = "done"
	} else if s.Cursor != nil {
		out.Backfill = "running"
	}
	return out
}

func (m *Module) getMetrics(w http.ResponseWriter, r *http.Request) {
	p, project, ok := metricProject(w, r)
	if !ok {
		return
	}
	now := m.now().UTC()
	out := DeliveryMetrics{ProjectID: project, GeneratedAt: now}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := projectSettings(r.Context(), tx, project); err != nil {
			return err
		}
		if err := requireMetrics(r.Context(), tx, p, "delivery.read", project); err != nil {
			return err
		}
		s, err := loadMetricSourceTx(r.Context(), tx, project, false)
		if err != nil {
			return err
		}
		if s == nil {
			out.Metrics = computeMetrics(metricInput{CIWorkflow: defaultCIWorkflow, NightlyWorkflow: defaultNightlyWorkflow}, now)
			reason := "No repository is linked to this project yet."
			for i := range out.Metrics {
				out.Metrics[i].Reason = &reason
			}
			return nil
		}
		in, err := loadMetricInputTx(r.Context(), tx, *s, now)
		if err != nil {
			return err
		}
		if err = loadProjectInputTx(r.Context(), tx, project, &in, now); err != nil {
			return err
		}
		// A disconnected App no longer observes the repository. Coverage ends
		// at the last stored GitHub fact or the finished backfill, so a window
		// that runs through now cannot stay complete.
		if !m.appConnected(p.TenantID, s.Repository) {
			end := githubCoverageEnd(*s, in, now)
			in.CoveredUntil = &end
		}
		out.Source = m.sourceView(p.TenantID, *s, in.Covered)
		out.Metrics = computeMetrics(in, now)
		if !out.Source.AppConnected {
			note := "The GitHub App of this workspace does not receive events for " + s.Repository + "; numbers come only from earlier facts and reports."
			for i := range out.Metrics {
				if !strings.Contains(out.Metrics[i].Source, "GitHub App") {
					continue
				}
				if out.Metrics[i].Status == "ok" {
					out.Metrics[i].Status = "partial"
					for j := range out.Metrics[i].Windows {
						if out.Metrics[i].Windows[j].Status == "ok" {
							out.Metrics[i].Windows[j].Status = "partial"
						}
					}
				}
				appendMetricReason(&out.Metrics[i], note)
			}
		}
		return nil
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func (m *Module) putMetricSource(w http.ResponseWriter, r *http.Request) {
	p, project, ok := metricProject(w, r)
	if !ok {
		return
	}
	var in metricSourceInput
	if err := decodeBounded(w, r, 4<<10, &in); err != nil {
		respondError(w, err)
		return
	}
	ci, nightly := defaultCIWorkflow, defaultNightlyWorkflow
	if in.CIWorkflow != nil {
		ci = *in.CIWorkflow
	}
	if in.NightlyWorkflow != nil {
		nightly = *in.NightlyWorkflow
	}
	if !reviewgate.ValidRepository(in.Repository) || !metricWorkflow.MatchString(ci) || !metricWorkflow.MatchString(nightly) {
		respondError(w, fail(400, "invalid metrics source"))
		return
	}
	var out *MetricSource
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(r.Context(), tx, p.TenantID); err != nil {
			return err
		}
		if _, err := projectSettings(r.Context(), tx, project); err != nil {
			return err
		}
		if err := requireMetrics(r.Context(), tx, p, "delivery.manage", project); err != nil {
			return err
		}
		// A new repository or workflow starts its history over: the stored
		// backfill position would claim coverage it never read.
		now := m.now()
		if _, err := tx.Exec(r.Context(), `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository,ci_workflow,nightly_workflow,updated_at) VALUES($1,$2,$3,$4,$5,$6)
			ON CONFLICT(tenant_id,project_id) DO UPDATE SET repository=EXCLUDED.repository,ci_workflow=EXCLUDED.ci_workflow,nightly_workflow=EXCLUDED.nightly_workflow,updated_at=EXCLUDED.updated_at,
			backfill_cursor=CASE WHEN (delivery_metric_sources.repository,delivery_metric_sources.ci_workflow,delivery_metric_sources.nightly_workflow)=(EXCLUDED.repository,EXCLUDED.ci_workflow,EXCLUDED.nightly_workflow) THEN delivery_metric_sources.backfill_cursor END,
			backfill_since=CASE WHEN (delivery_metric_sources.repository,delivery_metric_sources.ci_workflow,delivery_metric_sources.nightly_workflow)=(EXCLUDED.repository,EXCLUDED.ci_workflow,EXCLUDED.nightly_workflow) THEN delivery_metric_sources.backfill_since END,
			backfill_done_at=CASE WHEN (delivery_metric_sources.repository,delivery_metric_sources.ci_workflow,delivery_metric_sources.nightly_workflow)=(EXCLUDED.repository,EXCLUDED.ci_workflow,EXCLUDED.nightly_workflow) THEN delivery_metric_sources.backfill_done_at END,
			backfill_revision=delivery_metric_sources.backfill_revision+1`,
			p.TenantID, project, in.Repository, ci, nightly, now); err != nil {
			return err
		}
		s, err := loadMetricSourceTx(r.Context(), tx, project, false)
		if err != nil {
			return err
		}
		if s == nil {
			return pgx.ErrNoRows
		}
		out = m.sourceView(p.TenantID, *s, nil)
		return nil
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func (m *Module) backfillMetrics(w http.ResponseWriter, r *http.Request) {
	p, project, ok := metricProject(w, r)
	if !ok {
		return
	}
	in := backfillInput{}
	if err := decodeBounded(w, r, 1<<10, &in); err != nil {
		respondError(w, err)
		return
	}
	days := metricDays
	if in.Days != nil {
		days = *in.Days
	}
	if days < 1 || days > 90 {
		respondError(w, fail(400, "backfill days must be 1 to 90"))
		return
	}
	reader, readable := m.github.(metricsReader)
	now := m.now().UTC()
	var source *metricSourceRow
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := projectSettings(r.Context(), tx, project); err != nil {
			return err
		}
		if err := requireMetrics(r.Context(), tx, p, "delivery.manage", project); err != nil {
			return err
		}
		var err error
		source, err = loadMetricSourceTx(r.Context(), tx, project, false)
		return err
	})
	if err != nil {
		respondError(w, err)
		return
	}
	if source == nil {
		respondError(w, fail(409, "link a repository to this project first"))
		return
	}
	if !readable || !m.appConnected(p.TenantID, source.Repository) {
		respondError(w, fail(409, "the GitHub App is not connected to this repository"))
		return
	}
	cursor := source.Cursor
	if cursor == nil || in.Restart {
		since := dayStart(now.AddDate(0, 0, -days))
		cursor = &backfillCursor{Since: since, Phase: "runs", Day: since.Format("2006-01-02"), Page: 1}
	}
	result := BackfillResult{Since: cursor.Since, Phase: cursor.Phase, Truncated: cursor.Truncated}
	if cursor.Phase == "done" && source.DoneAt != nil {
		// The runs and pulls are in; the jobs pass reads what the check suites
		// did not bring (AEON-1016) until no eligible run lacks job facts.
		result, err = m.fillJobs(r.Context(), p, project, source, reader, cursor, now)
		if err != nil {
			respondError(w, err)
			return
		}
		httpapi.WriteJSON(w, 200, result)
		return
	}
	workflows := []string{source.CIWorkflow}
	if source.NightlyWorkflow != source.CIWorkflow {
		workflows = append(workflows, source.NightlyWorkflow)
	}
	next := *cursor
	var batch backfillBatch
	err = reader.MetricsRead(r.Context(), p.TenantID, func(get func(string, any) error) error {
		var err error
		next, batch, err = backfillStep(get, source.Repository, workflows, *cursor, now)
		return err
	})
	if err != nil {
		// Nothing is written: the stored cursor stays, and a retry repeats
		// the same pages.
		respondError(w, fail(502, "GitHub could not be read; the backfill resumes from its last stored position"))
		return
	}
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(r.Context(), tx, p.TenantID); err != nil {
			return err
		}
		if _, err := projectSettings(r.Context(), tx, project); err != nil {
			return err
		}
		if err := requireMetrics(r.Context(), tx, p, "delivery.manage", project); err != nil {
			return err
		}
		current, err := loadMetricSourceTx(r.Context(), tx, project, true)
		if err != nil {
			return err
		}
		if current == nil || current.Revision != source.Revision || current.Repository != source.Repository {
			return fail(409, "the backfill moved or the source changed meanwhile; call again to continue")
		}
		at := m.now()
		if err = upsertRunsTx(r.Context(), tx, p.TenantID, source.Repository, "backfill", batch.Runs, at); err != nil {
			return err
		}
		if err = upsertPullsTx(r.Context(), tx, p.TenantID, source.Repository, "backfill", batch.Pulls, at); err != nil {
			return err
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		var done *time.Time
		if next.Phase == "done" {
			done = &at
		}
		_, err = tx.Exec(r.Context(), `UPDATE delivery_metric_sources SET backfill_cursor=$2,backfill_revision=backfill_revision+1,backfill_since=$3,backfill_done_at=$4 WHERE project_id=$1`, project, raw, next.Since, done)
		return err
	})
	if err != nil {
		respondError(w, err)
		return
	}
	result = BackfillResult{State: "running", Since: next.Since, Phase: next.Phase, Calls: batch.Calls, Runs: len(batch.Runs), Pulls: len(batch.Pulls), Truncated: next.Truncated}
	if next.Phase == "runs" {
		day := next.Day
		result.Day = &day
	}
	if next.Phase == "done" {
		result.State = "done"
	}
	httpapi.WriteJSON(w, 200, result)
}

// validFact checks one reported fact. Timing facts become marks; review audits
// and escaped defects (AEON-1016) become reports. Exactly one of them is set.
func validFact(f metricFactInput, now time.Time) (metricMark, *metricReport, error) {
	k := metricMark{Kind: f.Kind, Key: f.Key, PR: f.PullRequest, Started: f.StartedAt, At: f.At.UTC(), Outcome: f.Outcome}
	if !metricFactKey.MatchString(f.Key) || f.At.IsZero() || f.At.After(now.Add(5*time.Minute)) || f.At.Before(now.AddDate(0, 0, -400)) ||
		f.PullRequest != nil && (*f.PullRequest < 1 || *f.PullRequest > 2147483647) || f.StartedAt != nil && (f.StartedAt.After(f.At) || f.At.Sub(*f.StartedAt) > 30*24*time.Hour) {
		return k, nil, fail(400, "invalid delivery metric fact")
	}
	if f.HeadSHA != nil {
		if !reviewgate.ValidSHA(*f.HeadSHA) {
			return k, nil, fail(400, "invalid delivery metric fact head")
		}
		k.Head = *f.HeadSHA
	}
	if f.StartedAt != nil {
		v := f.StartedAt.UTC()
		k.Started = &v
	}
	// Release facts (AEON-1001): name, tag and the time the live version was
	// healthy again, only on a release, and healthy only after it went live.
	// An escaped defect (AEON-1016) may name the release that caused it.
	if f.Kind != "release" && (f.Tag != nil || f.HealthyAt != nil) || f.Kind != "release" && f.Kind != "escaped_defect" && f.Release != nil ||
		f.Release != nil && !metricReleaseName.MatchString(*f.Release) || f.Tag != nil && !metricReleaseTag.MatchString(*f.Tag) {
		return k, nil, fail(400, "release, tag and healthy_at belong to a release fact")
	}
	if f.HealthyAt != nil {
		if f.Outcome != "live" || f.HealthyAt.Before(f.At) || f.HealthyAt.After(now.Add(5*time.Minute)) || f.HealthyAt.Sub(f.At) > 30*24*time.Hour {
			return k, nil, fail(400, "healthy_at needs a live release and lies between at and now")
		}
		v := f.HealthyAt.UTC()
		k.Healthy = &v
	}
	k.Release, k.Tag = f.Release, f.Tag
	switch f.Kind {
	case "review":
		if f.StartedAt == nil || f.Outcome != "ok" && f.Outcome != "changes" {
			return k, nil, fail(400, "a review needs started_at and outcome ok or changes")
		}
	case "merge_round":
		if f.PullRequest == nil || f.Outcome != "scripted" && f.Outcome != "model" {
			return k, nil, fail(400, "a merge round needs pull_request and outcome scripted or model")
		}
	case "release":
		if f.StartedAt == nil || f.Outcome != "live" && f.Outcome != "failed" {
			return k, nil, fail(400, "a release needs started_at and outcome live or failed")
		}
	case "review_audit":
		// The audit of a merged pull request; the outcome is the highest severity
		// among its findings, clean when it found none.
		if f.PullRequest == nil || f.Outcome != "clean" && f.Outcome != "low" && f.Outcome != "medium" && f.Outcome != "high" {
			return k, nil, fail(400, "a review audit needs pull_request and outcome clean, low, medium or high")
		}
		severity := f.Outcome
		if severity == "clean" {
			severity = "none"
		}
		return k, &metricReport{Kind: f.Kind, Key: f.Key, PR: f.PullRequest, Severity: severity, Started: k.Started, At: k.At}, nil
	case "escaped_defect":
		// The key names the bug or incident ticket; the link says what caused
		// it: a merged pull request, a release, or both. Without that link the
		// defect is not "caused by" anything and does not count.
		if f.PullRequest == nil && f.Release == nil || f.Outcome != "low" && f.Outcome != "medium" && f.Outcome != "high" {
			return k, nil, fail(400, "an escaped defect needs pull_request or release, and outcome low, medium or high")
		}
		return k, &metricReport{Kind: f.Kind, Key: f.Key, PR: f.PullRequest, Release: f.Release, Severity: f.Outcome, Started: k.Started, At: k.At}, nil
	default:
		return k, nil, fail(400, "fact kind must be review, merge_round, release, review_audit or escaped_defect")
	}
	return k, nil, nil
}

func (m *Module) reportMetricFacts(w http.ResponseWriter, r *http.Request) {
	p, project, ok := metricProject(w, r)
	if !ok {
		return
	}
	var in metricFactsInput
	if err := decodeBounded(w, r, 64<<10, &in); err != nil {
		respondError(w, err)
		return
	}
	if len(in.Facts) < 1 || len(in.Facts) > 100 {
		respondError(w, fail(400, "report 1 to 100 facts"))
		return
	}
	now := m.now()
	marks := make([]metricMark, 0, len(in.Facts))
	var reports []metricReport
	seen := map[string]bool{}
	for _, f := range in.Facts {
		k, report, err := validFact(f, now)
		if err != nil {
			respondError(w, err)
			return
		}
		if seen[k.Kind+"\x00"+k.Key] {
			respondError(w, fail(400, "duplicate fact key"))
			return
		}
		seen[k.Kind+"\x00"+k.Key] = true
		if report != nil {
			reports = append(reports, *report)
			continue
		}
		marks = append(marks, k)
	}
	stored := 0
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(r.Context(), tx, p.TenantID); err != nil {
			return err
		}
		if _, err := projectSettings(r.Context(), tx, project); err != nil {
			return err
		}
		if err := requireMetrics(r.Context(), tx, p, "delivery.manage", project); err != nil {
			return err
		}
		s, err := loadMetricSourceTx(r.Context(), tx, project, false)
		if err != nil {
			return err
		}
		if s == nil {
			return fail(409, "link a repository to this project first")
		}
		if err = upsertMarksTx(r.Context(), tx, p.TenantID, s.Repository, "report", marks, now); err != nil {
			return err
		}
		if err = upsertReportsTx(r.Context(), tx, p.TenantID, s.Repository, reports, now); err != nil {
			return err
		}
		stored = len(marks) + len(reports)
		return nil
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]int{"stored": stored})
}

const (
	jobFillBudget = 40 // GitHub reads per step
	jobFillRetry  = time.Hour
)

// fillJobs reads the jobs of attempt 1 of pull-request and merge-queue runs of
// the CI workflow that have none yet, newest first, within one step's budget.
// A run whose jobs GitHub could not return is tried again after an hour, so a
// few persistent failures never block the rest; its window stays partial and
// says so. Authority is checked again in the write transaction.
func (m *Module) fillJobs(ctx context.Context, p tenant.Principal, project string, source *metricSourceRow, reader metricsReader, cursor *backfillCursor, now time.Time) (BackfillResult, error) {
	result := BackfillResult{State: "running", Since: cursor.Since, Phase: "jobs", Truncated: cursor.Truncated}
	var runs []int64
	var required []string
	from := windowSpan(now, metricLongDays, true).first.Add(-metricSlack)
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := requireMetrics(ctx, tx, p, "delivery.manage", project); err != nil {
			return err
		}
		var err error
		if required, err = requiredChecksTx(ctx, tx, []string{project}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT run_id FROM delivery_metric_runs
			WHERE repository=$1 AND attempt=1 AND jobs_read_at IS NULL AND event IN ('pull_request','merge_group') AND completed_at>=$2
			AND regexp_replace(workflow_path,'^.*/','')=$3 AND (jobs_failed_at IS NULL OR jobs_failed_at<$4)
			ORDER BY completed_at DESC LIMIT $5`, source.Repository, from, path.Base(source.CIWorkflow), now.Add(-jobFillRetry), jobFillBudget)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				return err
			}
			runs = append(runs, id)
		}
		return rows.Err()
	})
	if err != nil {
		return result, err
	}
	if len(runs) == 0 {
		result.State, result.Phase = "done", "done"
		return result, nil
	}
	facts := map[int64]jobFacts{}
	var failedRuns []int64
	calls := 0
	err = reader.MetricsRead(ctx, p.TenantID, func(get func(string, any) error) error {
		counted := func(path string, out any) error { calls++; return get(path, out) }
		for _, id := range runs {
			if calls >= jobFillBudget {
				break
			}
			f, err := readJobFacts(counted, id, 1, required)
			if err != nil {
				failedRuns = append(failedRuns, id)
				continue
			}
			facts[id] = f
		}
		return nil
	})
	// Nothing read at all is GitHub's fault, not the runs': report it and keep
	// every run eligible. Failures among successes mark only those runs.
	if err != nil || len(facts) == 0 {
		return result, fail(502, "GitHub could not be read; the jobs pass resumes from the runs that still lack job facts")
	}
	result.Calls = calls
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if _, err := projectSettings(ctx, tx, project); err != nil {
			return err
		}
		if err := requireMetrics(ctx, tx, p, "delivery.manage", project); err != nil {
			return err
		}
		current, err := loadMetricSourceTx(ctx, tx, project, true)
		if err != nil {
			return err
		}
		if current == nil || current.Revision != source.Revision || current.Repository != source.Repository {
			return fail(409, "the backfill moved or the source changed meanwhile; call again to continue")
		}
		at := m.now()
		for id, f := range facts {
			raw, err := json.Marshal(f.Required)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE delivery_metric_runs SET jobs_read_at=$4,jobs_failed_at=NULL,worst_job_wait_ms=$5,required_jobs=$6 WHERE repository=$1 AND run_id=$2 AND attempt=$3`,
				source.Repository, id, 1, at, f.WorstWaitMS, raw); err != nil {
				return err
			}
		}
		for _, id := range failedRuns {
			if _, err = tx.Exec(ctx, `UPDATE delivery_metric_runs SET jobs_failed_at=$3 WHERE repository=$1 AND run_id=$2 AND attempt=1`, source.Repository, id, at); err != nil {
				return err
			}
		}
		result.Jobs = len(facts)
		return nil
	})
	return result, err
}
