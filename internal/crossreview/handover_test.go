// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func TestCompletedBuilderDuringWorkHandover(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		ancestor   bool
	}{
		{"split", "split", false},
		{"cancel", "cancel", false},
		{"cancel ancestor", "cancel", true},
	} {
		for _, enabled := range []bool{false, true} {
			name := tc.name + "/status disabled"
			if enabled {
				name = tc.name + "/status enabled"
			}
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				nodes.New(f.d.App, nil).Mount(f.mux)
				project, target := testID(), f.ticket
				f.tx(t, func(tx pgx.Tx) error {
					if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title)
 SELECT $1,$2,'PROJECT-1',id,'Handover project' FROM node_kinds WHERE slug='project'`, f.person.TenantID, project); err != nil {
						return err
					}
					parent := project
					if tc.ancestor {
						target = testID()
						if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id,state)
 SELECT $1,$2,'PARENT-1',id,'Parent work',$3,'open' FROM node_kinds WHERE slug='work'`, f.person.TenantID, target, project); err != nil {
							return err
						}
						parent = target
					}
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2,state='open' WHERE id=$1`, f.ticket, parent)
					return err
				})
				if enabled {
					dbtest.EnableWorkParentStatus(t, f.d, f.person.TenantID)
				}
				var order workorders.Order
				f.call(t, f.agent, "POST", "/api/work-orders", map[string]any{"title": "Builder", "parent_id": f.ticket, "assignee_principal_id": f.agent.ID, "criteria": []string{"Pass isolation tests"}}, 201, &order)
				var run, session string
				f.tx(t, func(tx pgx.Tx) error {
					if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_model,status,account_id,daemon_id,daemon_generation,started_at)
 SELECT $1,$2,$3,p.id,p.model,'running',$4,'review-daemon','review-generation',clock_timestamp() FROM model_profiles p WHERE p.family='openai' AND p.version='test-version' RETURNING id::text`, f.person.TenantID, order.NodeID, f.agent.ID, f.account).Scan(&run); err != nil {
						return err
					}
					return tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,work_order_id,run_id,harness,host,management,role,work_shape,capabilities,ref_digest,lease_digest,phase,activity,owner_principal_id)
 VALUES($1,$2,$3,$4,$5,$6,'codex','test','unmanaged','worker','ship',ARRAY['inbox','pause'],decode(replace(gen_random_uuid()::text,'-',''),'hex'),decode(replace(gen_random_uuid()::text,'-',''),'hex'),'working','busy',$7) RETURNING id::text`, f.person.TenantID, project, f.agent.ID, f.ticket, order.NodeID, run, f.person.ID).Scan(&session)
				})
				path := "/api/nodes/" + target + "/work-lifecycle"
				var preview struct {
					Busy          bool      `json:"busy"`
					UpdatedAt     time.Time `json:"updated_at"`
					OpenLeaves    int       `json:"open_leaves"`
					ScopeRevision string    `json:"scope_revision"`
				}
				f.call(t, f.person, "GET", path, nil, 200, &preview)
				if !preview.Busy || preview.OpenLeaves != 1 {
					t.Fatalf("fixture did not establish one busy open leaf: %+v", preview)
				}
				request := testID()
				input := map[string]any{"request_id": request, "kind": tc.kind, "expected_updated_at": preview.UpdatedAt, "expected_open_leaves": preview.OpenLeaves, "expected_scope_revision": preview.ScopeRevision}
				if tc.kind == "split" {
					input["children"] = []map[string]string{{"title": "First child"}, {"title": "Second child"}}
				}
				type action struct {
					State        string   `json:"state"`
					WaitingCount int      `json:"waiting_count"`
					Result       []string `json:"result"`
				}
				var waiting action
				f.call(t, f.person, "POST", path, input, 200, &waiting)
				if waiting.State != "waiting" || waiting.WaitingCount != 1 || len(waiting.Result) != 0 {
					t.Fatalf("action did not wait for builder handover: %+v", waiting)
				}
				// Confirm the original generation stopped, leaving the live run as
				// the only blocker. A terminal report must still be able to commit.
				f.tx(t, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),stop_reason='completed',phase='stopped' WHERE id=$1`, session)
					return err
				})
				continuePath := path + "/" + request + "/continue"
				f.call(t, f.person, "POST", continuePath, nil, 200, &waiting)
				if waiting.State != "waiting" || waiting.WaitingCount != 1 {
					t.Fatal("action completed before the builder reported terminal")
				}
				report := agentruns.Telemetry{Sequence: 1, Kind: "finished", Status: "completed", Input: 17, Output: 9, Cost: 123,
					ReviewRange: &reviewgate.CommitRange{Repository: "example/review-fixture", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40)}}
				telemetryPath := "/api/runs/" + run + "/telemetry"
				f.call(t, f.agent, "POST", telemetryPath, report, 200, nil)
				f.call(t, f.agent, "POST", telemetryPath, report, 200, nil)
				var finished action
				f.call(t, f.person, "POST", continuePath, nil, 200, &finished)
				wantResults := 1
				if tc.kind == "split" {
					wantResults = 2
				}
				if finished.State != "completed" || finished.WaitingCount != 0 || len(finished.Result) != wantResults {
					t.Fatalf("action stranded after builder completion: %+v", finished)
				}
				var replay action
				f.call(t, f.person, "POST", continuePath, nil, 200, &replay)
				if replay.State != "completed" || !slices.Equal(replay.Result, finished.Result) {
					t.Fatalf("lifecycle replay changed results: %+v", replay)
				}
				f.call(t, f.agent, "POST", telemetryPath, report, 200, nil)
				var reviews []Review
				f.call(t, f.person, "GET", "/api/nodes/"+f.ticket+"/reviews", nil, 200, &reviews)
				if len(reviews) != 0 {
					t.Fatalf("handover queued a review: %+v", reviews)
				}
				f.tx(t, func(tx pgx.Tx) error {
					var status, parent, reason, state string
					var ended, leaf bool
					var input, output, cost int64
					if err := tx.QueryRow(t.Context(), `SELECT r.status,r.ended_at IS NOT NULL,r.input_tokens,r.output_tokens,r.cost_micros,n.parent_id::text
 FROM agent_runs r JOIN nodes n ON n.id=r.work_order_id WHERE r.id=$1`, run).Scan(&status, &ended, &input, &output, &cost, &parent); err != nil {
						return err
					}
					if status != "completed" || !ended || input != report.Input || output != report.Output || cost != report.Cost || parent != f.ticket {
						t.Fatalf("completion, usage or original binding lost: status=%s ended=%v usage=%d/%d/%d parent=%s", status, ended, input, output, cost, parent)
					}
					var telemetry, unavailable, orders int
					if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM run_telemetry WHERE run_id=$1),
 (SELECT count(*) FROM events WHERE node_id=$2 AND type='review.unavailable' AND after->>'author_run_id'=$1::text),
 (SELECT count(*) FROM work_orders)`, run, f.ticket).Scan(&telemetry, &unavailable, &orders); err != nil {
						return err
					}
					if telemetry != 1 || unavailable != 1 || orders != 1 {
						t.Fatalf("replay or partial review writes: telemetry=%d unavailable=%d orders=%d", telemetry, unavailable, orders)
					}
					if err := tx.QueryRow(t.Context(), `SELECT after->>'reason' FROM events WHERE node_id=$1 AND type='review.unavailable' AND after->>'author_run_id'=$2`, f.ticket, run).Scan(&reason); err != nil {
						return err
					}
					if !strings.Contains(reason, "handover") || !strings.Contains(reason, "gate remains closed") {
						t.Fatalf("review unavailable for wrong reason: %s", reason)
					}
					if err := tx.QueryRow(t.Context(), `SELECT state,aeon_work_leaf(id) FROM nodes WHERE id=$1`, f.ticket).Scan(&state, &leaf); err != nil {
						return err
					}
					if tc.kind == "split" && leaf || tc.kind == "cancel" && (state != "cancelled" || !leaf || finished.Result[0] != f.ticket) {
						t.Fatalf("lifecycle result not applied: state=%s leaf=%v", state, leaf)
					}
					return nil
				})
			})
		}
	}
}
