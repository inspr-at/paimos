// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func handoffFixture(t *testing.T) (*fixture, string, qEntry, map[string]any) {
	f := setup(t)
	f.agent.Scopes = []string{"run.claim", "run.telemetry", "run.read"}
	f.queueAccount(t, 1000000)
	project, session := uuid(), uuid()
	lease := "fixture-lead-lease-private-0000000000000"
	leaseDigest := sha256.Sum256([]byte("aeon.harness.lease\x00" + lease))
	refDigest := sha256.Sum256([]byte("aeon.harness.ref\x00fixture-lead-reference"))
	coordinator := f.other
	coordinator.Scopes = authz.CoordinatorKeyScopes
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'lead_queue_test','Lead') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
			return err
		}
		for _, permission := range authz.CoordinatorBaseScopes {
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, f.person.TenantID, role, permission); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1`, coordinator.ID, role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'LQ-1',id,'Lead project' FROM node_kinds WHERE slug='project'`, f.person.TenantID, project); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,phase,heartbeat_at,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,'codex','local','unmanaged','coordinator','working',clock_timestamp(),$6,$7)`, f.person.TenantID, session, project, coordinator.ID, f.person.ID, refDigest[:], leaseDigest[:]); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,generation,state) VALUES($1,$2,$3,$4,1,'working')`, f.person.TenantID, project, f.person.ID, session)
		return err
	})
	id := f.ticket(t, "open", "high", nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, id, project)
		return err
	})
	e := f.addQueue(t, id, nil)

	admission := func(ctx context.Context, tx pgx.Tx, _ tenant.Principal, _ string, _ harness.Session) (harness.LeadChecks, error) {
		var now time.Time
		err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
		g := harness.LeadGate{State: "available", CheckedAt: now}
		return harness.LeadChecks{Dial: g, Harness: g, Account: g, Host: g}, err
	}
	f.mux = http.NewServeMux()
	workorders.New(f.d.App).Mount(f.mux)
	agentruns.NewWithLeadAdmission(f.d.App, admission).Mount(f.mux)
	r := httptest.NewRequest("POST", "/api/queue/next", strings.NewReader(`{"agent_principal_id":"`+f.agent.ID+`","model_profile_id":"`+f.profile+`"}`)).WithContext(tenant.WithPrincipal(t.Context(), coordinator))
	r.Header.Set("X-Aeon-Lead-Session", session)
	r.Header.Set("X-Aeon-Lead-Generation", strconv.Itoa(1))
	r.Header.Set("X-Aeon-Worker-Lease", lease)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("route: %d %s", w.Code, w.Body.String())
	}
	body := claimBody(f.reserve(t, e.Run))
	var h agentruns.WorkerHandoff
	f.call(t, f.person, "GET", "/api/runs/"+e.Run.ID+"/handoff", nil, 200, &h)
	if h.RunID != e.Run.ID || h.TicketID != id || h.State != "assigned" || h.LeadSessionID != session || h.LeadGeneration != 1 {
		t.Fatalf("assignment: %+v", h)
	}
	body["handoff"] = agentruns.WorkerPickup{TicketRevision: h.TicketRevision, WorkOrderRevision: h.WorkOrderRevision, BriefSHA256: h.BriefSHA256, WorktreeID: strings.Repeat("a", 64)}
	return f, project, e, body
}

func TestWorkerHandoffReceiptsReplayAndSuccession(t *testing.T) {
	f, project, e, body := handoffFixture(t)
	path := "/api/runs/" + e.Run.ID
	f.call(t, f.agent, "POST", path+"/claim", body, 200, nil)
	n := f.count(t, f.person, `SELECT count(*) FROM events WHERE type='run.claimed'`)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE project_leads SET generation=2,state='paused' WHERE project_id=$1`, project)
		return err
	})
	f.call(t, f.agent, "POST", path+"/claim", body, 200, nil)
	if f.count(t, f.person, `SELECT count(*) FROM events WHERE type='run.claimed'`) != n {
		t.Fatal("claim replay wrote another receipt")
	}
	var h agentruns.WorkerHandoff
	f.call(t, f.person, "GET", path+"/handoff", nil, 200, &h)
	if h.State != "launch_unknown" || h.AcceptedAt == nil || h.LeadGeneration != 1 || h.LaunchedAt != nil {
		t.Fatal("succession transferred the unknown writer")
	}
	divergent := body["handoff"].(agentruns.WorkerPickup)
	divergent.WorktreeID = strings.Repeat("b", 64)
	body["handoff"] = divergent
	w := f.request(f.agent, "POST", path+"/claim", mustHandoffJSON(t, body), f.token)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "assignment pickup conflict") {
		t.Fatalf("divergent pickup: %d %s", w.Code, w.Body.String())
	}
	started := map[string]any{"sequence": 1, "kind": "started"}
	f.call(t, f.agent, "POST", path+"/telemetry", started, 200, nil)
	f.call(t, f.agent, "POST", path+"/telemetry", started, 200, nil)
	f.call(t, f.person, "GET", path+"/handoff", nil, 200, &h)
	if h.State != "launched" || h.LaunchedAt == nil {
		t.Fatal("launch receipt missing")
	}
	f.call(t, f.agent, "POST", path+"/telemetry", map[string]any{"sequence": 2, "kind": "finished"}, 409, nil)
	f.call(t, f.agent, "POST", path+"/telemetry", map[string]any{"sequence": 2, "kind": "finished", "process_state": "not_attempted"}, 409, nil)
	finished := map[string]any{"sequence": 2, "kind": "finished", "process_state": "exited"}
	f.call(t, f.agent, "POST", path+"/telemetry", finished, 200, nil)
	f.call(t, f.agent, "POST", path+"/telemetry", finished, 200, nil)
	divergentReport := map[string]any{"sequence": 2, "kind": "finished", "process_state": "not_attempted"}
	w = f.request(f.agent, "POST", path+"/telemetry", mustHandoffJSON(t, divergentReport), f.token)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "divergent telemetry replay") {
		t.Fatalf("divergent exit proof: %d %s", w.Code, w.Body.String())
	}
	f.call(t, f.person, "GET", path+"/handoff", nil, 200, &h)
	if h.State != "completed" || h.CompletedAt == nil || h.Outcome != "completed" || h.LeadGeneration != 1 {
		t.Fatal("completion lost the original generation")
	}
	f.call(t, f.foreign, "GET", path+"/handoff", nil, 404, nil)
	f.agent.Scopes = []string{"run.claim"}
	f.call(t, f.agent, "GET", path+"/handoff", nil, 403, nil)
}

func TestWorkerHandoffRejectsChangedBriefAndMissingPickup(t *testing.T) {
	for _, change := range []string{"pickup", "ticket", "brief", "order", "permission", "role", "legacy"} {
		t.Run(change, func(t *testing.T) {
			f, _, e, body := handoffFixture(t)
			want, reason := 409, "assignment brief or ticket changed"
			switch change {
			case "pickup":
				delete(body, "handoff")
				want, reason = 400, "exact lead assignment pickup required"
			case "permission":
				f.agent.Scopes = []string{"run.read"}
				want, reason = 403, "missing_key_scope"
			case "legacy":
				delete(body, "handoff")
				reason = "durable lead assignment required"
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET trace=trace-'worker_assignment' WHERE id=$1`, e.Run.ID)
					return err
				})
			case "role":
				want, reason = 403, "missing_role_permission"
				f.tx(t, f.person, func(tx pgx.Tx) error {
					var role string
					if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'handoff_read_only','Read only') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
						return err
					}
					if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read'),($1,$2,'run.read')`, f.person.TenantID, role); err != nil {
						return err
					}
					_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1`, f.agent.ID, role)
					return err
				})
			default:
				f.tx(t, f.person, func(tx pgx.Tx) error {
					q, id := `UPDATE nodes SET updated_at=updated_at+interval '1 second' WHERE id=$1`, e.NodeID
					if change == "brief" {
						q, id = `UPDATE nodes SET body='changed brief' WHERE id=$1`, e.Run.OrderID
					} else if change == "order" {
						q, id = `UPDATE work_orders SET revision=revision+1 WHERE node_id=$1`, e.Run.OrderID
					}
					_, err := tx.Exec(t.Context(), q, id)
					return err
				})
			}
			w := f.request(f.agent, "POST", "/api/runs/"+e.Run.ID+"/claim", mustHandoffJSON(t, body), f.token)
			if w.Code != want || !strings.Contains(w.Body.String(), reason) {
				t.Fatalf("%s: %d %s", change, w.Code, w.Body.String())
			}
			if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND status='queued' AND (trace->'worker_assignment'->>'state'='assigned' OR NOT trace ? 'worker_assignment')`, e.Run.ID) != 1 {
				t.Fatal("failed pickup partially committed")
			}
		})
	}
}

func TestWorkerHandoffUnknownExitBlocksRetryAndReconciles(t *testing.T) {
	f, _, e, body := handoffFixture(t)
	path := "/api/runs/" + e.Run.ID
	f.call(t, f.agent, "POST", path+"/claim", body, 200, nil)
	f.call(t, f.person, "POST", path+"/cancel", map[string]any{}, 409, nil)
	f.call(t, f.agent, "POST", path+"/telemetry", map[string]any{"sequence": 1, "kind": "finished", "status": "ownership_lost", "process_state": "unconfirmed"}, 200, nil)
	w := f.request(f.person, "POST", "/api/queue", mustHandoffJSON(t, map[string]any{"node_id": e.NodeID}), "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "previous writer exit or launch outcome is unconfirmed") {
		t.Fatalf("unknown exit allowed requeue: %d %s", w.Code, w.Body.String())
	}
	f.call(t, f.person, "POST", "/api/work-orders/"+e.Run.OrderID+"/runs", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "retry_of_run_id": e.Run.ID}, 409, nil)
	f.call(t, f.agent, "POST", path+"/telemetry", map[string]any{"sequence": 2, "kind": "finished", "status": "failed", "process_state": "exited"}, 200, nil)
	var h agentruns.WorkerHandoff
	f.call(t, f.person, "GET", path+"/handoff", nil, 200, &h)
	if h.State != "completed" || h.CompletedAt == nil || h.Outcome != "failed" {
		t.Fatal("confirmed exit did not reconcile")
	}
	f.addQueue(t, e.NodeID, nil)
}

// Pause only after the first transaction checked the checkout while holding
// the tenant/tree fence. The competitor must be observed waiting in Postgres
// before that transaction is allowed to persist acceptance and commit.
type handoffClaimPause struct {
	first  atomic.Bool
	locked chan uint32
	resume chan struct{}
}
type handoffClaimPauseKey struct{}

func (p *handoffClaimPause) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.Contains(q.SQL, "trace->'worker_assignment'->>'worktree_id'=$2") && p.first.CompareAndSwap(false, true) {
		return context.WithValue(ctx, handoffClaimPauseKey{}, true)
	}
	return ctx
}
func (p *handoffClaimPause) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, q pgx.TraceQueryEndData) {
	if ctx.Value(handoffClaimPauseKey{}) == true && q.Err == nil {
		p.locked <- conn.PgConn().PID()
		select {
		case <-p.resume:
		case <-ctx.Done():
		}
	}
}

func secondWorkerHandoff(t *testing.T, f *fixture, project string, first qEntry) (qEntry, map[string]any) {
	t.Helper()
	id := f.ticket(t, "open", "high", nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, id, project)
		return err
	})
	second := f.addQueue(t, id, nil)
	// This distinct worker also leads in the fixture. Give its live role an
	// explicit claim grant; coordinator read/dispatch scopes alone cannot start.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,role_id,'run.claim' FROM role_bindings WHERE principal_id=$2 ON CONFLICT DO NOTHING`, f.person.TenantID, f.other.ID)
		return err
	})
	account := f.queueAccount(t, 1000000)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET registered_by_principal_id=$2 WHERE id=$1`, account, f.other.ID)
		return err
	})
	var lead agentruns.WorkerHandoff
	f.call(t, f.person, "GET", "/api/runs/"+first.Run.ID+"/handoff", nil, 200, &lead)
	coordinator := f.other
	coordinator.Scopes = authz.CoordinatorKeyScopes
	r := httptest.NewRequest("POST", "/api/queue/next", strings.NewReader(`{"agent_principal_id":"`+f.other.ID+`","model_profile_id":"`+f.profile+`"}`)).WithContext(tenant.WithPrincipal(t.Context(), coordinator))
	r.Header.Set("X-Aeon-Lead-Session", lead.LeadSessionID)
	r.Header.Set("X-Aeon-Lead-Generation", "1")
	r.Header.Set("X-Aeon-Worker-Lease", "fixture-lead-lease-private-0000000000000")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("second route: %d %s", w.Code, w.Body.String())
	}
	var selected struct{ Entry qEntry }
	if err := json.Unmarshal(w.Body.Bytes(), &selected); err != nil || selected.Entry.Run.ID != second.Run.ID {
		t.Fatalf("fixture did not route distinct assignment: %s", w.Body.String())
	}
	var h agentruns.WorkerHandoff
	f.call(t, f.person, "GET", "/api/runs/"+second.Run.ID+"/handoff", nil, 200, &h)
	body := claimBody(f.reserve(t, second.Run))
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET registered_by_principal_id=$2 WHERE id=(SELECT account_id FROM agent_runs WHERE id=$1)`, second.Run.ID, f.other.ID)
		return err
	})
	body["handoff"] = agentruns.WorkerPickup{TicketRevision: h.TicketRevision, WorkOrderRevision: h.WorkOrderRevision, BriefSHA256: h.BriefSHA256, WorktreeID: strings.Repeat("a", 64)}
	return second, body
}

func TestWorkerHandoffConcurrentPickupOneWriter(t *testing.T) {
	for _, distinct := range []bool{false, true} {
		name := "same-run"
		if distinct {
			name = "distinct-runs-same-checkout"
		}
		t.Run(name, func(t *testing.T) {
			f, project, first, body := handoffFixture(t)
			second, competingBody, principal, token := first, claimBody(body["reservation_ids"].([]string)), f.agent, f.token
			reason := "assignment pickup conflict"
			if distinct {
				second, competingBody = secondWorkerHandoff(t, f, project, first)
				principal = f.other
				principal.Scopes = []string{"run.claim"}
				token = f.key(t, principal, principal.Scopes)
				reason = "assignment worktree already has an unconfirmed writer"
			} else {
				pickup := body["handoff"].(agentruns.WorkerPickup)
				pickup.WorktreeID = strings.Repeat("b", 64)
				competingBody["handoff"] = pickup
			}
			pause := &handoffClaimPause{locked: make(chan uint32, 1), resume: make(chan struct{})}
			cfg := f.d.App.Config()
			cfg.ConnConfig.Tracer = pause
			pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			// Remount the actual claim handler with its normal lead admission path.
			f.mux = http.NewServeMux()
			agentruns.NewWithLeadAdmission(pool, func(ctx context.Context, tx pgx.Tx, _ tenant.Principal, _ string, _ harness.Session) (harness.LeadChecks, error) {
				var now time.Time
				err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
				gate := harness.LeadGate{State: "available", CheckedAt: now}
				return harness.LeadChecks{Dial: gate, Harness: gate, Account: gate, Host: gate}, err
			}).Mount(f.mux)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			firstDone, secondDone := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
			t.Cleanup(func() { cancel(); pool.Close() })
			defer func() {
				select {
				case <-pause.resume:
				default:
					close(pause.resume)
				}
			}()
			request := func(p tenant.Principal, run string, raw, key string) *httptest.ResponseRecorder {
				r := httpRequest(ctx, p, "POST", "/api/runs/"+run+"/claim", raw)
				r.Header.Set("Authorization", "Bearer "+key)
				w := httptest.NewRecorder()
				f.mux.ServeHTTP(w, r)
				return w
			}
			firstRaw, secondRaw := mustHandoffJSON(t, body), mustHandoffJSON(t, competingBody)
			go func() { firstDone <- request(f.agent, first.Run.ID, firstRaw, f.token) }()
			var pid uint32
			select {
			case pid = <-pause.locked:
			case w := <-firstDone:
				t.Fatalf("first did not reach acceptance fence: %d %s", w.Code, w.Body.String())
			case <-ctx.Done():
				t.Fatal("first never reached fence")
			}
			go func() { secondDone <- request(principal, second.Run.ID, secondRaw, token) }()
			for {
				var waiting bool
				err := f.d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) AND query=$2)`, pid, `SELECT id FROM tenants WHERE id=current_setting('aeon.tenant_id')::uuid FOR NO KEY UPDATE`).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case w := <-secondDone:
					t.Fatalf("competitor crossed held fence: %d %s", w.Code, w.Body.String())
				case <-ctx.Done():
					t.Fatal("competitor never waited for tenant fence")
				default:
				}
			}
			close(pause.resume)
			for _, result := range []struct {
				done   <-chan *httptest.ResponseRecorder
				want   int
				reason string
			}{{firstDone, 200, ""}, {secondDone, 409, reason}} {
				select {
				case w := <-result.done:
					if w.Code != result.want || !strings.Contains(w.Body.String(), result.reason) {
						t.Fatalf("claim result: %d %s", w.Code, w.Body.String())
					}
				case <-ctx.Done():
					t.Fatal("claims failed to finish")
				}
			}
			if f.count(t, f.person, `SELECT count(*) FROM events WHERE type='run.claimed'`) != 1 || f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE trace->'worker_assignment'->>'state'='launch_unknown'`) != 1 {
				t.Fatal("overlapping pickup admitted multiple writers")
			}
			if distinct && f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND status='queued' AND trace->'worker_assignment'->>'state'='assigned' AND NOT(trace->'worker_assignment' ? 'accepted_at')`, second.Run.ID) != 1 {
				t.Fatal("rejected competing assignment partially accepted")
			}
		})
	}
}

func TestWorkerHandoffWorktreeIsHeldUntilExit(t *testing.T) {
	f, project, first, body := handoffFixture(t)
	f.call(t, f.agent, "POST", "/api/runs/"+first.Run.ID+"/claim", body, 200, nil)
	id := f.ticket(t, "open", "high", nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, id, project)
		return err
	})
	second := f.addQueue(t, id, nil)
	var lead agentruns.WorkerHandoff
	f.call(t, f.person, "GET", "/api/runs/"+first.Run.ID+"/handoff", nil, 200, &lead)
	coordinator := f.other
	coordinator.Scopes = authz.CoordinatorKeyScopes
	r := httptest.NewRequest("POST", "/api/queue/next", strings.NewReader(`{"agent_principal_id":"`+f.agent.ID+`","model_profile_id":"`+f.profile+`"}`)).WithContext(tenant.WithPrincipal(t.Context(), coordinator))
	r.Header.Set("X-Aeon-Lead-Session", lead.LeadSessionID)
	r.Header.Set("X-Aeon-Lead-Generation", "1")
	r.Header.Set("X-Aeon-Worker-Lease", "fixture-lead-lease-private-0000000000000")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("second route: %d %s", w.Code, w.Body.String())
	}
	var selected struct{ Entry qEntry }
	if err := json.Unmarshal(w.Body.Bytes(), &selected); err != nil || selected.Entry.Run.ID != second.Run.ID {
		t.Fatal("fixture did not route the second run")
	}
	var h agentruns.WorkerHandoff
	f.call(t, f.person, "GET", "/api/runs/"+second.Run.ID+"/handoff", nil, 200, &h)
	pickup := claimBody(f.reserve(t, second.Run))
	pickup["handoff"] = agentruns.WorkerPickup{TicketRevision: h.TicketRevision, WorkOrderRevision: h.WorkOrderRevision, BriefSHA256: h.BriefSHA256, WorktreeID: lead.WorktreeID}
	w = f.request(f.agent, "POST", "/api/runs/"+second.Run.ID+"/claim", mustHandoffJSON(t, pickup), f.token)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "assignment worktree already has an unconfirmed writer") {
		t.Fatalf("worktree fence: %d %s", w.Code, w.Body.String())
	}
	f.call(t, f.agent, "POST", "/api/runs/"+first.Run.ID+"/telemetry", map[string]any{"sequence": 1, "kind": "finished", "status": "failed", "process_state": "not_attempted"}, 200, nil)
	f.call(t, f.agent, "POST", "/api/runs/"+second.Run.ID+"/claim", pickup, 200, nil)
}

func mustHandoffJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestWorkerHandoffObsoleteClaimDoesNotAcceptWriter(t *testing.T) {
	for _, reason := range []string{"quota_pool_changed", "account_moved_out_of_group"} {
		t.Run(reason, func(t *testing.T) {
			f, _, e, body := handoffFixture(t)
			f.tx(t, f.agent, func(tx pgx.Tx) error {
				if reason == "account_moved_out_of_group" {
					group := uuid()
					if _, err := tx.Exec(t.Context(), `INSERT INTO account_groups(tenant_id,id,harness,name) VALUES($1,$2,'codex','Changed group')`, f.agent.TenantID, group); err != nil {
						return err
					}
					_, err := tx.Exec(t.Context(), `INSERT INTO account_run_targets(tenant_id,run_id,group_id) VALUES($1,$2,$3)`, f.agent.TenantID, e.Run.ID, group)
					return err
				}
				sibling := uuid()
				if _, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label) VALUES($1,$2::uuid,$2::text,'codex','sibling',$3,'Sibling')`, f.agent.TenantID, sibling, f.other.ID); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET account_id=$2 WHERE id IN(SELECT window_id FROM account_reservations WHERE run_id=$1)`, e.Run.ID, sibling)
				return err
			})
			path := "/api/runs/" + e.Run.ID
			w := f.request(f.agent, "POST", path+"/claim", mustHandoffJSON(t, body), f.token)
			if w.Code != 409 || !strings.Contains(w.Body.String(), reason) {
				t.Fatalf("obsolete claim: %d %s", w.Code, w.Body.String())
			}
			var h agentruns.WorkerHandoff
			f.call(t, f.person, "GET", path+"/handoff", nil, 200, &h)
			if h.State != "assigned" || h.WorktreeID != "" || h.AcceptedAt != nil {
				t.Fatalf("rejected claim persisted phantom writer: %+v", h)
			}
			if f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='active'`, e.Run.ID) != 0 || f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND status='queued' AND account_id IS NULL`, e.Run.ID) != 1 {
				t.Fatal("obsolete reservation was not released")
			}
			if f.count(t, f.person, `SELECT count(*) FROM events WHERE type='run.claimed'`) != 0 {
				t.Fatal("rejected claim wrote acceptance event")
			}
			f.call(t, f.person, "POST", path+"/cancel", map[string]any{}, 200, nil)
			f.addQueue(t, e.NodeID, nil)
		})
	}
}
