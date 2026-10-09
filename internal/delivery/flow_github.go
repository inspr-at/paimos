// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/jackc/pgx/v5"
)

// flowStepsFromRuns maps completed workflow run attempts of one pull request
// to Flow steps: pull_request runs are checks (ci), merge_group runs are the
// merge queue. A failed attempt that a later attempt of the same run passed
// on the same commit is flaky; a re-run is a repeat. The wait in the merge
// queue before a run starts and the wait between attempts are waits.
func flowStepsFromRuns(runs []metricRun) []flowStepInput {
	sorted := append([]metricRun(nil), runs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].ID != sorted[j].ID {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].Attempt < sorted[j].Attempt
	})
	passed := map[int64]map[string]int{}
	for _, r := range sorted {
		if r.Conclusion == "success" {
			if passed[r.ID] == nil {
				passed[r.ID] = map[string]int{}
			}
			passed[r.ID][r.Head] = r.Attempt
		}
	}
	var out []flowStepInput
	byAttempt := map[[2]int64]metricRun{}
	for _, r := range sorted {
		byAttempt[[2]int64{r.ID, int64(r.Attempt)}] = r
		stepKey, actor := "ci", FlowActor{Type: "ci", Label: "Checks"}
		if r.Event == "merge_group" {
			stepKey, actor = "queue", FlowActor{Type: "queue", Label: "Checks"}
		}
		kind := "work"
		if r.Attempt > 1 {
			kind = "rework"
		}
		var outcome *string
		switch {
		case r.Conclusion == "success":
			outcome = flowStr("green")
		case failed(r.Conclusion) && passed[r.ID][r.Head] > r.Attempt:
			outcome = flowStr("flaky")
		case failed(r.Conclusion):
			outcome = flowStr("red")
		}
		key := fmt.Sprintf("run/%d/%d", r.ID, r.Attempt)
		if r.Event == "merge_group" && r.Attempt == 1 && r.Started.After(r.Created) {
			started := r.Started
			out = append(out, flowStepInput{Source: "github_app", Key: key + "/queued", StepKey: "queue", Round: 1, Kind: "wait", Actor: FlowActor{Type: "queue", Label: "Checks"}, Started: r.Created, Ended: &started, WaitReason: flowStr("queue")})
		}
		if prev, ok := byAttempt[[2]int64{r.ID, int64(r.Attempt - 1)}]; ok && r.Started.After(prev.Completed) {
			started := r.Started
			out = append(out, flowStepInput{Source: "github_app", Key: key + "/rerun", StepKey: stepKey, Round: r.Attempt, Kind: "wait", Actor: actor, Started: prev.Completed, Ended: &started, WaitReason: flowStr("rerun")})
		}
		completed := r.Completed
		round := r.Attempt
		if round > 1000 {
			round = 1000
		}
		out = append(out, flowStepInput{Source: "github_app", Key: key, StepKey: stepKey, Round: round, Kind: kind, Actor: actor, Started: r.Started, Ended: &completed, Outcome: outcome})
	}
	return out
}

// flowFromRuns records checks and merge-queue runs of pull requests that
// PAIMOS links to a ticket. It runs after the metric facts committed; an
// error returns 502 to GitHub and the redelivery converges on the same rows.
func (m *Module) flowFromRuns(ctx context.Context, runs []metricRun) error {
	byPR := map[int64][]metricRun{}
	var prs []int64
	for _, r := range runs {
		if r.PR == nil || r.Event != "pull_request" && r.Event != "merge_group" {
			continue
		}
		if byPR[*r.PR] == nil {
			prs = append(prs, *r.PR)
		}
		byPR[*r.PR] = append(byPR[*r.PR], r)
	}
	if len(prs) == 0 {
		return nil
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i] < prs[j] })
	service := db.AllProjects(ctx, "delivery flow: GitHub checks and merge-queue runs of linked changes")
	_, err := m.flowWrite(service, m.config.TenantID, nil, func(ctx context.Context, tx pgx.Tx, apply func([]flowBatch) error) error {
		for _, pr := range prs {
			var project, ticket string
			err := tx.QueryRow(ctx, `SELECT project_id::text,ticket_node_id::text FROM delivery_items WHERE repository=$1 AND pull_request=$2 AND project_id IS NOT NULL AND ticket_node_id IS NOT NULL`,
				m.config.Repository, pr).Scan(&project, &ticket)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			ci := defaultCIWorkflow
			if err = tx.QueryRow(ctx, `SELECT ci_workflow FROM delivery_metric_sources WHERE project_id=$1 AND repository=$2`, project, m.config.Repository).Scan(&ci); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			var own []metricRun
			for _, r := range byPR[pr] {
				if sameWorkflow(r, ci) {
					own = append(own, r)
				}
			}
			t, err := flowTicketTx(ctx, tx, ticket)
			if err != nil {
				return err
			}
			if len(own) == 0 || t == nil || t.Project != project {
				continue
			}
			batch := flowBatch{Item: flowItemInput{Project: project, Kind: "change", Ref: t.Key, Title: t.Title, Ticket: &ticket, PRs: []int64{pr}, Repository: m.config.Repository}, Steps: flowStepsFromRuns(own)}
			if err := apply([]flowBatch{batch}); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

// flowLiveRun is one workflow-run transition. Phase is queued, running or
// completed. Completed is the only phase with an end; metric facts stay on
// the completed check-suite path.
type flowLiveRun struct {
	ID         int64
	Attempt    int
	Event      string
	Workflow   string
	Created    time.Time
	Started    time.Time
	Completed  time.Time
	Conclusion string
	Phase      string
	PR         int64
	Head       string
}

// flowLiveRunFrom reads one workflow_run delivery. skip means the run is
// well formed and not a linked check or merge-queue run. A malformed run is
// errRead so GitHub redelivers it.
func flowLiveRunFrom(raw []byte) (flowLiveRun, bool, error) {
	var e struct {
		Action string `json:"action"`
		Run    struct {
			ID         int64      `json:"id"`
			Attempt    int        `json:"run_attempt"`
			Name       string     `json:"name"`
			Path       string     `json:"path"`
			Event      string     `json:"event"`
			Branch     string     `json:"head_branch"`
			Head       string     `json:"head_sha"`
			Status     string     `json:"status"`
			Conclusion *string    `json:"conclusion"`
			Created    time.Time  `json:"created_at"`
			Started    *time.Time `json:"run_started_at"`
			Updated    time.Time  `json:"updated_at"`
			Pulls      []struct {
				Number int64 `json:"number"`
			} `json:"pull_requests"`
		} `json:"workflow_run"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return flowLiveRun{}, false, errRead
	}
	r := e.Run
	if r.ID <= 0 || r.Attempt < 1 || r.Attempt > 1000 || r.Name == "" || len(r.Name) > 200 || r.Path == "" || len(r.Path) > 255 || r.Event == "" || len(r.Event) > 64 || len(r.Branch) > 255 || !reviewgate.ValidSHA(r.Head) || r.Created.IsZero() || len(r.Pulls) > 100 {
		return flowLiveRun{}, false, errRead
	}
	phase := ""
	switch e.Action {
	case "requested":
		if r.Status == "in_progress" || r.Status == "completed" {
			return flowLiveRun{}, false, errRead
		}
		phase = "queued"
	case "in_progress":
		if r.Status != "" && r.Status != "in_progress" {
			return flowLiveRun{}, false, errRead
		}
		phase = "running"
	case "completed":
		if (r.Status != "" && r.Status != "completed") || r.Conclusion == nil || *r.Conclusion == "" || len(*r.Conclusion) > 32 || r.Updated.IsZero() {
			return flowLiveRun{}, false, errRead
		}
		phase = "completed"
	default:
		return flowLiveRun{}, false, errRead
	}
	if r.Event != "pull_request" && r.Event != "merge_group" {
		return flowLiveRun{}, true, nil
	}
	var pr int64
	if len(r.Pulls) > 0 && r.Pulls[0].Number > 0 {
		pr = r.Pulls[0].Number
	} else if match := queueBranch.FindStringSubmatch(r.Branch); match != nil {
		pr, _ = strconv.ParseInt(match[1], 10, 64)
	}
	if pr <= 0 {
		return flowLiveRun{}, true, nil
	}
	started := r.Created
	if r.Started != nil && !r.Started.IsZero() {
		started = *r.Started
	}
	out := flowLiveRun{ID: r.ID, Attempt: r.Attempt, Event: r.Event, Workflow: r.Path, Created: r.Created.UTC(), Started: started.UTC(), Phase: phase, PR: pr, Head: r.Head}
	if phase == "completed" {
		out.Completed = r.Updated.UTC()
		out.Conclusion = *r.Conclusion
	}
	return out, false, nil
}

func flowLiveWork(key, stepKey string, round int, kind string, actor FlowActor, start time.Time, ended *time.Time, outcome *string) flowStepInput {
	return flowStepInput{Source: "github_app", Key: key, StepKey: stepKey, Round: round, Kind: kind, Actor: actor, Started: start, Ended: ended, Outcome: outcome, Progress: ended == nil}
}

func flowQueueWait(key string, started time.Time, ended *time.Time) flowStepInput {
	return flowStepInput{Source: "github_app", Key: key + "/queued", StepKey: "queue", Round: 1, Kind: "wait", Actor: FlowActor{Type: "queue", Label: "Checks"}, Started: started, Ended: ended, WaitReason: flowStr("queue"), Progress: ended == nil}
}

// flowLiveSteps maps one workflow-run transition. A queued or running check
// stays open. Completion uses the same keys as a finished suite run; a single
// failure is red until that suite reports a later pass as flaky.
func flowLiveSteps(r flowLiveRun) []flowStepInput {
	stepKey, actor := "ci", FlowActor{Type: "ci", Label: "Checks"}
	if r.Event == "merge_group" {
		stepKey, actor = "queue", FlowActor{Type: "queue", Label: "Checks"}
	}
	kind := "work"
	if r.Attempt > 1 {
		kind = "rework"
	}
	round := r.Attempt
	if round > 1000 {
		round = 1000
	}
	if round < 1 {
		round = 1
	}
	key := fmt.Sprintf("run/%d/%d", r.ID, r.Attempt)
	start := r.Started
	if start.IsZero() {
		start = r.Created
	}
	switch r.Phase {
	case "queued":
		if r.Event == "merge_group" {
			step := flowQueueWait(key, r.Created, nil)
			step.Phase = "queued"
			return []flowStepInput{step}
		}
		step := flowLiveWork(key, stepKey, round, kind, actor, start, nil, nil)
		step.Phase = "queued"
		return []flowStepInput{step}
	case "running":
		steps := []flowStepInput{}
		if r.Event == "merge_group" {
			ended := start
			steps = append(steps, flowQueueWait(key, r.Created, &ended))
		}
		return append(steps, flowLiveWork(key, stepKey, round, kind, actor, start, nil, nil))
	case "completed":
		steps := flowStepsFromRuns([]metricRun{{ID: r.ID, Attempt: r.Attempt, Event: r.Event, Created: r.Created, Started: start, Completed: r.Completed, Conclusion: r.Conclusion}})
		if r.Event == "merge_group" {
			queued := key + "/queued"
			found := false
			for _, s := range steps {
				if s.Key == queued {
					found = true
					break
				}
			}
			if !found {
				ended := start
				steps = append(steps, flowQueueWait(key, r.Created, &ended))
			}
		}
		return steps
	default:
		return nil
	}
}

// flowKnownAttemptsTx reads completed attempts of one run already stored as
// metric facts. A live completion of a single attempt classifies from these,
// so an earlier failure stays flaky after the suite saw a later pass.
func flowKnownAttemptsTx(ctx context.Context, tx pgx.Tx, repository string, runID int64) ([]metricRun, error) {
	rows, err := tx.Query(ctx, `SELECT attempt,event,head_sha,created_at,started_at,completed_at,conclusion FROM delivery_metric_runs WHERE repository=$1 AND run_id=$2 ORDER BY attempt`, repository, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metricRun
	for rows.Next() {
		var r metricRun
		if err := rows.Scan(&r.Attempt, &r.Event, &r.Head, &r.Created, &r.Started, &r.Completed, &r.Conclusion); err != nil {
			return nil, err
		}
		r.ID = runID
		out = append(out, r)
	}
	return out, rows.Err()
}

// flowLiveStepsKeepingClass writes this transition's steps, with outcomes
// recomputed from every known attempt of the run. The attempt in the event
// fills a gap the metric facts do not have yet.
func flowLiveStepsKeepingClass(run flowLiveRun, known []metricRun) []flowStepInput {
	cur := metricRun{ID: run.ID, Attempt: run.Attempt, Event: run.Event, Head: run.Head, Created: run.Created, Started: run.Started, Completed: run.Completed, Conclusion: run.Conclusion, PR: &run.PR}
	merged := make([]metricRun, 0, len(known)+1)
	seen := false
	for _, k := range known {
		if k.Attempt == run.Attempt {
			merged = append(merged, cur)
			seen = true
			continue
		}
		merged = append(merged, k)
	}
	if !seen {
		merged = append(merged, cur)
	}
	classified := flowStepsFromRuns(merged)
	outcome := map[string]*string{}
	for _, s := range classified {
		outcome[s.Key] = s.Outcome
	}
	steps := flowLiveSteps(run)
	for i := range steps {
		if o, ok := outcome[steps[i].Key]; ok {
			steps[i].Outcome = o
		}
	}
	return steps
}

// flowFromWorkflowRun records a queued, running or completed CI or merge-queue
// run. It does not write metric facts; those stay completed-only.
func (m *Module) flowFromWorkflowRun(ctx context.Context, raw []byte) error {
	run, skip, err := flowLiveRunFrom(raw)
	if err != nil || skip {
		return err
	}
	steps := flowLiveSteps(run)
	if len(steps) == 0 {
		return nil
	}
	service := db.AllProjects(ctx, "delivery flow: live GitHub checks and merge-queue runs of linked changes")
	_, err = m.flowWrite(service, m.config.TenantID, nil, func(ctx context.Context, tx pgx.Tx, apply func([]flowBatch) error) error {
		var project, ticket string
		err := tx.QueryRow(ctx, `SELECT project_id::text,ticket_node_id::text FROM delivery_items WHERE repository=$1 AND pull_request=$2 AND project_id IS NOT NULL AND ticket_node_id IS NOT NULL`,
			m.config.Repository, run.PR).Scan(&project, &ticket)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		ci := defaultCIWorkflow
		if err = tx.QueryRow(ctx, `SELECT ci_workflow FROM delivery_metric_sources WHERE project_id=$1 AND repository=$2`, project, m.config.Repository).Scan(&ci); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if !sameWorkflow(metricRun{Workflow: run.Workflow}, ci) {
			return nil
		}
		t, err := flowTicketTx(ctx, tx, ticket)
		if err != nil || t == nil || t.Project != project {
			return err
		}
		writeSteps := steps
		if run.Phase == "completed" {
			known, err := flowKnownAttemptsTx(ctx, tx, m.config.Repository, run.ID)
			if err != nil {
				return err
			}
			writeSteps = flowLiveStepsKeepingClass(run, known)
		}
		batch := flowBatch{Item: flowItemInput{Project: project, Kind: "change", Ref: t.Key, Title: t.Title, Ticket: &ticket, PRs: []int64{run.PR}, Repository: m.config.Repository}, Steps: writeSteps}
		return apply([]flowBatch{batch})
	})
	return err
}
