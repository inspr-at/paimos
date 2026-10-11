// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Risks R15/R17: a queue SHA reusing PR CI/artifact evidence, unknown checks
// bypassing configured gates, or a recorded merge fabricating Done. Existing
// merge-done tests own responsible-person fallback and benefit refusal.
func TestRoutinePipelineRefreshesQueueProofAndObservesDone(t *testing.T) {
	f := newFixture(t)
	head, queue := strings.Repeat("b", 40), strings.Repeat("c", 40)
	facts := RoutinePipelineFacts{Facts: greenShipFacts(head), ArtifactSHA: head, ObservedAt: time.Now().UTC()}
	ready := func(want bool) {
		t.Helper()
		f.tx(t, func(tx pgx.Tx) error {
			_, got, err := RoutineReadyTx(t.Context(), tx, f.project, f.m.config.Repository, head, facts)
			if err == nil && got != want {
				t.Fatalf("ready=%v want %v facts=%+v", got, want, facts)
			}
			return err
		})
	}
	ready(true)
	facts.Facts.PullHead = strings.Repeat("d", 40)
	ready(false)
	facts.Facts.PullHead = head
	facts.Facts.Checks[0].Conclusion = "failure"
	ready(false)
	facts.Facts.Checks[0].Conclusion = "success"
	facts.ArtifactSHA = queue
	ready(false)
	facts.Facts.Queued = true
	facts.Facts.QueueHead = queue
	ready(false) // PR green cannot supply the new merge SHA's checks.
	facts.Facts.Runs = []ShipRun{{ID: 12, Attempt: 1, Head: queue, Event: "merge_group", Workflow: "CI", Status: "completed", Conclusion: "success", Jobs: append([]Check(nil), facts.Facts.Checks...)}}
	ready(true)
	facts.Facts.QueueHead = strings.Repeat("e", 40)
	ready(false)
	facts.Facts.QueueHead = queue
	facts.Facts.Runs[0].Jobs[0].Status = "in_progress"
	ready(false)
	facts.Facts.Runs[0].Jobs[0].Status = "completed"
	f.tx(t, func(tx pgx.Tx) error {
		done, reason, err := RoutineMergeResultTx(t.Context(), tx, f.ticket, f.m.config.Repository, head, queue, 7)
		if err == nil && (done || reason != "merge_done_receipt_pending_or_refused") {
			t.Fatal("merge without Done receipt falsely completed")
		}
		return err
	})
	// This is the exact observation emitted by the existing AEON-1008 writer,
	// not a new status mutation. An unrelated merge/head cannot match it.
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO events(tenant_id,node_id,actor_principal_id,type,metadata) VALUES($1,$2,$3,'delivery.merge_done',jsonb_build_object('repository',$4::text,'head_sha',$5::text,'merge_sha',$6::text,'pull_request',7))`, f.person.TenantID, f.ticket, f.person.ID, f.m.config.Repository, head, queue)
		return err
	})
	f.tx(t, func(tx pgx.Tx) error {
		done, _, err := RoutineMergeResultTx(t.Context(), tx, f.ticket, f.m.config.Repository, head, queue, 7)
		if err != nil {
			return err
		}
		if !done {
			t.Fatal("exact Done observation missing")
		}
		done, _, err = RoutineMergeResultTx(t.Context(), tx, f.ticket, f.m.config.Repository, head, strings.Repeat("f", 40), 7)
		if done {
			t.Fatal("wrong merge claimed Done")
		}
		return err
	})
}
