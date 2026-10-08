// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/inspr-at/paimos/internal/db"
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
			batch := flowBatch{Item: flowItemInput{Project: project, Kind: "change", Ref: t.Key, Title: t.Title, Ticket: &ticket, PRs: []int64{pr}}, Steps: flowStepsFromRuns(own)}
			if err := apply([]flowBatch{batch}); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}
