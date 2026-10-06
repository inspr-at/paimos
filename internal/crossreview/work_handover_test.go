// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func TestBuilderCompletionDuringWorkHandover(t *testing.T) {
	for _, kind := range []string{"split", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			nodes.New(f.d.App, nil).Mount(f.mux)
			var project string
			actionNode := f.ticket
			f.tx(t, func(tx pgx.Tx) error {
				if err := db.LockWorkTreeTx(t.Context(), tx); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title)
 SELECT $1,'PROJECT-1',id,'Handover project' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&project); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$1 WHERE id=$2`, project, f.ticket); err != nil {
					return err
				}
				if kind == "cancel" {
					// Cancellation on an ancestor must also suppress automatic review.
					return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id)
 SELECT $1,'REVIEW-2',kind_id,'Builder leaf',id FROM nodes WHERE id=$2 RETURNING id::text`, f.person.TenantID, actionNode).Scan(&f.ticket)
				}
				return nil
			})
			var order workorders.Order
			f.call(t, f.agent, "POST", "/api/work-orders", map[string]any{"title": "Builder", "parent_id": f.ticket, "assignee_principal_id": f.agent.ID, "criteria": []string{"Pass isolation tests"}}, 201, &order)
			var source, session string
			f.tx(t, func(tx pgx.Tx) error {
				if err := db.LockWorkTreeTx(t.Context(), tx); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_model,status,account_id,daemon_id,daemon_generation,started_at)
 SELECT $1,$2,$3,p.id,p.model,'running',$4,'review-daemon','review-generation',clock_timestamp() FROM model_profiles p WHERE p.family='openai' AND p.version='test-version' RETURNING id::text`, f.person.TenantID, order.NodeID, f.agent.ID, f.account).Scan(&source); err != nil {
					return err
				}
				return tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,work_order_id,run_id,harness,host,management,role,work_shape,capabilities,ref_digest,lease_digest,phase,activity,owner_principal_id)
 VALUES($1,$2,$3,$4,$5,$6,'codex','test','unmanaged','worker','ship',ARRAY['inbox','pause'],decode(replace(gen_random_uuid()::text,'-',''),'hex'),decode(replace(gen_random_uuid()::text,'-',''),'hex'),'working','busy',$7) RETURNING id::text`, f.person.TenantID, project, f.agent.ID, f.ticket, order.NodeID, source, f.person.ID).Scan(&session)
			})
			path := "/api/nodes/" + actionNode + "/work-lifecycle"
			var preview struct {
				UpdatedAt     time.Time `json:"updated_at"`
				OpenLeaves    int       `json:"open_leaves"`
				ScopeRevision string    `json:"scope_revision"`
			}
			f.call(t, f.person, "GET", path, nil, 200, &preview)
			requestID := testID()
			input := map[string]any{"request_id": requestID, "kind": kind, "expected_updated_at": preview.UpdatedAt, "expected_open_leaves": preview.OpenLeaves, "expected_scope_revision": preview.ScopeRevision}
			if kind == "split" {
				input["children"] = []map[string]string{{"title": "First"}, {"title": "Second"}}
			}
			var action struct {
				State        string   `json:"state"`
				WaitingCount int      `json:"waiting_count"`
				Result       []string `json:"result"`
			}
			f.call(t, f.person, "POST", path, input, 200, &action)
			if action.State != "waiting" || action.WaitingCount != 1 || len(action.Result) != 0 {
				t.Fatalf("handover did not wait for the live builder: %+v", action)
			}
			report := agentruns.Telemetry{Sequence: 1, Kind: "finished", Status: "completed", ReviewRange: &reviewgate.CommitRange{Repository: "example/review-fixture", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40)}}
			for i := 0; i < 2; i++ {
				var run agentruns.Run
				f.call(t, f.agent, "POST", "/api/runs/"+source+"/telemetry", report, 200, &run)
				if run.ID != source || run.Status != "completed" {
					t.Fatalf("completion/replay lost the terminal run: %+v", run)
				}
			}
			f.tx(t, func(tx pgx.Tx) error {
				var telemetry, audit, reviews int
				var reason string
				if err := tx.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM run_telemetry WHERE run_id=$1),
 (SELECT count(*) FROM events WHERE type='review.unavailable' AND after->>'author_run_id'=$1::text),
 (SELECT count(*) FROM work_order_reviews WHERE author_run_id=$1),
 coalesce((SELECT after->>'reason' FROM events WHERE type='review.unavailable' AND after->>'author_run_id'=$1::text LIMIT 1),'')`, source).Scan(&telemetry, &audit, &reviews, &reason); err != nil {
					return err
				}
				if telemetry != 1 || audit != 1 || reviews != 0 || !strings.Contains(reason, "handover") || !strings.Contains(reason, "gate remains closed") {
					t.Fatalf("completion/replay evidence: telemetry=%d unavailable=%d reviews=%d reason=%q", telemetry, audit, reviews, reason)
				}
				return nil
			})
			continuation := path + "/" + requestID + "/continue"
			f.call(t, f.person, "POST", continuation, nil, 200, &action)
			if action.State != "waiting" || action.WaitingCount != 1 {
				t.Fatal("completion invented a confirmed session stop")
			}
			// Model the original generation's confirmed exit, retaining its binding.
			f.tx(t, func(tx pgx.Tx) error {
				if err := db.LockWorkTreeTx(t.Context(), tx); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),stop_reason='completed',phase='stopped' WHERE id=$1`, session)
				return err
			})
			f.call(t, f.person, "POST", continuation, nil, 200, &action)
			want := 1
			if kind == "split" {
				want = 2
			}
			if action.State != "completed" || action.WaitingCount != 0 || len(action.Result) != want {
				t.Fatalf("confirmed handover failed to finish: %+v", action)
			}
			result := strings.Join(action.Result, ",")
			f.call(t, f.person, "POST", continuation, nil, 200, &action)
			if action.State != "completed" || strings.Join(action.Result, ",") != result {
				t.Fatal("handover replay changed its result")
			}
			f.tx(t, func(tx pgx.Tx) error {
				var completed int
				var savedRun, savedTicket string
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='work.lifecycle_completed' AND node_id=$1`, actionNode).Scan(&completed); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `SELECT run_id::text,ticket_node_id::text FROM harness_sessions WHERE id=$1`, session).Scan(&savedRun, &savedTicket); err != nil {
					return err
				}
				if completed != 1 || savedRun != source || savedTicket != f.ticket {
					t.Fatal("handover duplicated completion or rewrote history")
				}
				return nil
			})
		})
	}
}
