// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Only queue/start times and GitHub identities survive the job event. In
// particular no steps, logs, runner labels or runner names enter this table.
type metricJob struct {
	ID      int64      `json:"id"`
	Run     int64      `json:"run_id"`
	Attempt int        `json:"run_attempt"`
	Head    string     `json:"head_sha"`
	Created *time.Time `json:"created_at"`
	Started *time.Time `json:"started_at"`
}

func upsertMetricJobTx(ctx context.Context, tx pgx.Tx, tid, repository string, job metricJob) error {
	if _, err := tx.Exec(ctx, `INSERT INTO delivery_metric_jobs(tenant_id,repository,run_id,attempt,job_id,created_at,started_at)
		VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant_id,repository,run_id,attempt,job_id) DO UPDATE SET
		created_at=least(delivery_metric_jobs.created_at,EXCLUDED.created_at),started_at=least(delivery_metric_jobs.started_at,EXCLUDED.started_at)`,
		tid, repository, job.Run, job.Attempt, job.ID, job.Created, job.Started); err != nil {
		return err
	}
	// Job events can precede or follow the run-completed receipt. Only an
	// existing complete job list can tell whether all runner waits are known.
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT required_jobs FROM delivery_metric_runs WHERE repository=$1 AND run_id=$2 AND attempt=$3 AND jobs_read_at IS NOT NULL`, repository, job.Run, job.Attempt).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var stored storedJobFacts
	if err := json.Unmarshal(raw, &stored); err != nil {
		return err
	}
	if !stored.Complete || len(stored.Waits) == 0 {
		return nil
	}
	facts := jobFacts{Complete: true, Conclusions: stored.Conclusions, Waits: stored.Waits}
	if err := hydrateJobWaitsTx(ctx, tx, repository, job.Run, job.Attempt, &facts); err != nil {
		return err
	}
	raw, err = encodeStoredJobs(facts)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE delivery_metric_runs SET required_jobs=$4,worst_job_wait_ms=$5 WHERE repository=$1 AND run_id=$2 AND attempt=$3`, repository, job.Run, job.Attempt, raw, facts.WorstWaitMS)
	return err
}

// The REST jobs response need not contain created_at. Match each missing
// queue time to a signed job event by run, attempt and job ID. One observed
// fast job must never hide the unknown wait of another job.
func hydrateJobWaitsTx(ctx context.Context, tx pgx.Tx, repository string, run int64, attempt int, facts *jobFacts) error {
	if len(facts.Waits) == 0 {
		return nil // legacy facts have no per-job timing identities
	}
	if len(facts.Waits) > jobPageSize*jobPageLimit {
		return errRead
	}
	ids := make([]int64, 0, len(facts.Waits))
	for key, wait := range facts.Waits {
		if wait != nil {
			continue
		}
		if id, err := strconv.ParseInt(key, 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		facts.WorstWaitMS = worstJobWait(facts.Waits)
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT job_id::text,created_at,started_at FROM delivery_metric_jobs WHERE repository=$1 AND run_id=$2 AND attempt=$3 AND job_id=ANY($4::bigint[]) LIMIT 500`, repository, run, attempt, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var created, started *time.Time
		if err := rows.Scan(&id, &created, &started); err != nil {
			return err
		}
		if facts.Waits[id] == nil && created != nil && started != nil && !created.IsZero() && !started.IsZero() && !started.Before(*created) && started.Sub(*created) <= 24*24*time.Hour {
			wait := int(started.Sub(*created).Milliseconds())
			facts.Waits[id] = &wait
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	facts.WorstWaitMS = worstJobWait(facts.Waits)
	return nil
}

// liveMetricRuns uses the authenticated workflow identity, never a job's URL
// or workflow display name. Only a completed attempt can carry a complete job
// map. A job that finishes before its workflow is left for workflow_run, and
// missing job facts are retried by the durable history worker.
func (m *Module) liveMetricRuns(ctx context.Context, name string, raw []byte, scope jobScope) ([]metricRun, error) {
	var body struct {
		Action string    `json:"action"`
		Run    githubRun `json:"workflow_run"`
		Job    struct {
			Run     int64  `json:"run_id"`
			Attempt int    `json:"run_attempt"`
			Head    string `json:"head_sha"`
		} `json:"workflow_job"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return nil, errRead
	}
	if body.Action != "completed" {
		return nil, nil
	}
	var runs []metricRun
	if name == "workflow_run" {
		run, ok, err := metricRunFrom(body.Run)
		if err != nil || !ok {
			return nil, errRead
		}
		runs = []metricRun{run}
	}
	reader, ok := m.github.(metricsReader)
	if !ok {
		return runs, nil
	}
	err := reader.MetricsRead(ctx, m.config.TenantID, func(get func(string, any) error, source string) error {
		if source == historyChecks {
			// Checks history has no attempt or queue-time evidence. The signed
			// workflow_run still supplies its own exact completed attempt.
			return nil
		}
		if source != historyActions {
			return errRead
		}
		calls := 0
		counted := func(endpoint string, out any) error {
			if calls >= backfillCallBudget {
				return errRead
			}
			calls++
			return get(endpoint, out)
		}
		if name == "workflow_job" {
			if err := counted(fmt.Sprintf("/actions/runs/%d/attempts/%d", body.Job.Run, body.Job.Attempt), &body.Run); err != nil {
				return err
			}
			if body.Run.ID != body.Job.Run || body.Run.Attempt != body.Job.Attempt || body.Run.Head != body.Job.Head {
				return errRead
			}
			if body.Run.Status != "completed" {
				return nil
			}
		}
		var err error
		runs, err = runWithFirstAttempt(counted, body.Run)
		if err != nil {
			return err
		}
		attachJobFacts(counted, runs, scope.ci)
		return nil
	})
	return runs, err
}
