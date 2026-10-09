// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
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
		if _, err := tx.Exec(ctx, `INSERT INTO delivery_metric_runs(tenant_id,repository,run_id,attempt,workflow_path,workflow_name,event,head_branch,head_sha,pull_request,created_at,started_at,completed_at,conclusion,source,recorded_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
			ON CONFLICT(tenant_id,repository,run_id,attempt) DO UPDATE SET workflow_path=EXCLUDED.workflow_path,workflow_name=EXCLUDED.workflow_name,event=EXCLUDED.event,head_branch=EXCLUDED.head_branch,head_sha=EXCLUDED.head_sha,
			pull_request=coalesce(EXCLUDED.pull_request,delivery_metric_runs.pull_request),created_at=EXCLUDED.created_at,started_at=EXCLUDED.started_at,completed_at=EXCLUDED.completed_at,conclusion=EXCLUDED.conclusion`,
			tid, repository, r.ID, r.Attempt, r.Workflow, r.Name, r.Event, r.Branch, r.Head, r.PR, r.Created, r.Started, r.Completed, r.Conclusion, source, at); err != nil {
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
		err := reader.MetricsRead(ctx, m.config.TenantID, func(get func(string, any) error) error {
			var err error
			runs, err = suiteRuns(get, e.Suite.ID, e.Suite.Head)
			return err
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
	Repository, CIWorkflow, NightlyWorkflow string
	Cursor                                  *backfillCursor
	Revision                                int64
	Since, DoneAt                           *time.Time
}

func loadMetricSourceTx(ctx context.Context, tx pgx.Tx, project string, lock bool) (*metricSourceRow, error) {
	var s metricSourceRow
	var raw []byte
	query := `SELECT repository,ci_workflow,nightly_workflow,backfill_cursor,backfill_revision,backfill_since,backfill_done_at FROM delivery_metric_sources WHERE project_id=$1`
	if lock {
		query += ` FOR NO KEY UPDATE`
	}
	err := tx.QueryRow(ctx, query, project).Scan(&s.Repository, &s.CIWorkflow, &s.NightlyWorkflow, &raw, &s.Revision, &s.Since, &s.DoneAt)
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
	rows, err := tx.Query(ctx, `SELECT run_id,attempt,workflow_path,workflow_name,event,head_branch,head_sha,pull_request,created_at,started_at,completed_at,conclusion,source
		FROM delivery_metric_runs WHERE repository=$1 AND completed_at>=$2 ORDER BY completed_at DESC LIMIT $3`, s.Repository, from, metricRunLimit+1)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var r metricRun
		if err = rows.Scan(&r.ID, &r.Attempt, &r.Workflow, &r.Name, &r.Event, &r.Branch, &r.Head, &r.PR, &r.Created, &r.Started, &r.Completed, &r.Conclusion, &r.Source); err != nil {
			rows.Close()
			return in, err
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
	if s.Cursor != nil && s.Cursor.Truncated {
		in.Truncated = true
	}
	return in, nil
}
