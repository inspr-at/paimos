// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/jackc/pgx/v5"
)

// Preflight reuses AEON-1658's canonical run/attempt ingestion. These events
// retain the workflow's observed conclusion, not a new CI or job projection.
type preflightEvidence struct {
	Repository string    `json:"repository"`
	Run        int64     `json:"run_id"`
	Attempt    int       `json:"attempt"`
	Head       string    `json:"workflow_head_sha"`
	Created    time.Time `json:"created_at"`
	Completed  time.Time `json:"completed_at"`
	Conclusion string    `json:"conclusion"`
}

func preflightRuns(runs []metricRun) []metricRun {
	var out []metricRun
	for _, run := range runs {
		if run.ID > 0 && run.Attempt == 1 && sameWorkflow(run, defaultPreflightWorkflow) {
			out = append(out, run)
		}
	}
	return out
}

func preflightEvidenceTx(ctx context.Context, tx pgx.Tx, project, repository string, runs []metricRun) ([]events.Change, error) {
	var changes []events.Change
	for _, run := range preflightRuns(runs) {
		evidence := preflightEvidence{Repository: repository, Run: run.ID, Attempt: run.Attempt, Head: run.Head,
			Created: run.Created, Completed: run.Completed, Conclusion: run.Conclusion}
		raw, err := json.Marshal(evidence)
		if err != nil {
			return nil, err
		}
		var same bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE node_id=$1
 AND type='delivery.evidence.preflight' AND after=$2::jsonb)`, project, raw).Scan(&same); err != nil {
			return nil, err
		}
		if !same {
			changes = append(changes, events.Change{NodeID: &project, Type: "delivery.evidence.preflight", After: evidence})
		}
	}
	return changes, nil
}

// Metric evidence is observation, never an admission approval. Callers hold
// the tenant fence and authorize the current project before writing. All
// returned events must be appended after the caller's final resource lock.
type metricEvidence struct {
	Repository string          `json:"repository"`
	Fact       metricFactInput `json:"fact"`
}

func metricEvidenceTx(ctx context.Context, tx pgx.Tx, tid, project, repository string, facts []metricFactInput, now time.Time) ([]events.Change, error) {
	var changes []events.Change
	for _, f := range facts {
		mark, report, err := validFact(f, now)
		if err != nil {
			return nil, err
		}
		var same bool
		if report != nil {
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_metric_reports
 WHERE repository=$1 AND kind=$2 AND report_key=$3
 AND (pull_request,release_name,severity,started_at,at) IS NOT DISTINCT FROM ($4::bigint,$5::text,$6::text,$7::timestamptz,$8::timestamptz))`,
				repository, report.Kind, report.Key, report.PR, report.Release, report.Severity, report.Started, report.At).Scan(&same)
		} else {
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_metric_marks
 WHERE repository=$1 AND kind=$2 AND mark_key=$3
 AND (pull_request,head_sha,started_at,at,outcome,release_name,release_tag,healthy_at) IS NOT DISTINCT FROM
 ($4::bigint,$5::text,$6::timestamptz,$7::timestamptz,$8::text,$9::text,$10::text,$11::timestamptz))`,
				repository, mark.Kind, mark.Key, mark.PR, mark.Head, mark.Started, mark.At, mark.Outcome, mark.Release, mark.Tag, mark.Healthy).Scan(&same)
		}
		if err != nil {
			return nil, err
		}
		if same {
			continue
		}
		if report != nil {
			err = upsertReportsTx(ctx, tx, tid, repository, []metricReport{*report}, now)
		} else {
			err = upsertMarksTx(ctx, tx, tid, repository, "report", []metricMark{mark}, now)
		}
		if err != nil {
			return nil, err
		}
		changes = append(changes, events.Change{NodeID: &project, Type: "delivery.evidence." + f.Kind,
			After: metricEvidence{Repository: repository, Fact: f}})
	}
	return changes, nil
}

func rolloutMetricFact(in rolloutInput, now time.Time) (*metricFactInput, error) {
	// A healthy time, completed step or release publication alone is not a
	// live confirmation. Keep those reports in Flow without inventing a sample.
	if (in.Outcome != "live" && in.Outcome != "success") || in.CutAt == nil || in.LiveAt == nil {
		return nil, nil
	}
	f := metricFactInput{Kind: "release", Key: "rollout/" + in.Release, Release: &in.Release,
		StartedAt: in.CutAt, At: *in.LiveAt, HealthyAt: in.HealthyAt, Outcome: "live"}
	if in.Version != "" && metricReleaseTag.MatchString(in.Version) {
		f.Tag = &in.Version
	}
	_, _, err := validFact(f, now)
	return &f, err
}

// Harness submissions already emit authenticated coordinator_review.recorded
// events. Read that existing feed, including history consumed by an older Flow
// cursor, rather than ingesting GitHub jobs or creating another review store.
// Explicit project/repository predicates are required: review_runs has tenant
// RLS only. Timing facts never become merge/review authority.
func loadHarnessMetricInputTx(ctx context.Context, tx pgx.Tx, s metricSourceRow, in *metricInput, now time.Time) error {
	from := windowSpan(now, metricLongDays, true).first.Add(-metricSlack)
	rows, err := tx.Query(ctx, `SELECT r.id::text,r.pull_request,r.head_sha,r.base_sha,r.target_branch,r.scope,
 r.created_at,r.submitted_at,r.verdict,r.author_family,r.reviewer_family
 FROM review_runs r
 WHERE r.project_id=$1 AND r.repository=$2 AND r.kind='review'
 AND r.submitted_at>=$3 AND r.submitted_at<=$4 AND r.created_at<=r.submitted_at
 AND r.model<>'' AND EXISTS(SELECT 1 FROM events e
 WHERE e.node_id=r.ticket_node_id AND e.type='coordinator_review.recorded'
 AND e.after->>'run_id'=r.id::text AND e.actor_principal_id=r.reviewer_principal_id)
 ORDER BY r.submitted_at DESC,r.id LIMIT $5`, s.Project, s.Repository, from, now, metricFactLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var mark metricMark
		var base, target, scope, author, reviewer string
		if err := rows.Scan(&mark.Key, &mark.PR, &mark.Head, &base, &target, &scope, &mark.Started, &mark.At, &mark.Outcome, &author, &reviewer); err != nil {
			return err
		}
		n++
		if n > metricFactLimit {
			in.ReadFrom = later(in.ReadFrom, mark.At.UTC().Add(metricSlack))
			in.Truncated = true
			break
		}
		binding := reviewgate.RunBinding{Repository: s.Repository, PR: *mark.PR, Target: target, Base: base, Head: mark.Head, Scope: scope}
		if !binding.Valid() || !reviewgate.ValidFamily(author) || !reviewgate.ValidFamily(reviewer) || author == reviewer || mark.Outcome != "ok" && mark.Outcome != "changes" {
			continue
		}
		mark.Kind, mark.Key, mark.Source = "review", "review_run/"+mark.Key, "paimos"
		in.Marks = append(in.Marks, mark)
	}
	return rows.Err()
}
