// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

func buildRegistration(f *harnessFixture, label string) map[string]any {
	return map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "builder-host", "management_mode": "unmanaged",
		"role": "worker", "ticket_node_id": f.ticket, "work_shape": "ship", "display_label": label,
		"harness_session_ref": "build-reference-" + uid(), "worker_lease": "build-worker-lease-" + uid(),
	}
}

// Risk: a correction round is refused as a rebuild, or the exception admits a
// different branch/active writer. The audit must be atomic and replay-safe.
func TestTicketDeliveryContinuationAdmissionAndAudit(t *testing.T) {
	for _, source := range []string{"pull", "branch"} {
		t.Run(source, func(t *testing.T) {
			f := fixture(t)
			base := "/api/projects/" + f.project + "/harness-sessions"
			const repository, branch = "owner/linked", "work/hts-2-built"
			f.tx(t, f.person, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository,ci_workflow,nightly_workflow,updated_at) VALUES($1,$2,$3,'CI','Nightly',clock_timestamp())`, f.person.TenantID, f.project, repository); err != nil {
					return err
				}
				if source == "pull" {
					if _, err := tx.Exec(t.Context(), `INSERT INTO delivery_items(tenant_id,id,project_id,ticket_node_id,repository,branch,head_sha,state,state_since,owner,observation,updated_at) VALUES($1,$2,$3,$4,$5,$6,repeat('c',40),'built','2000-01-01','coordinator','{"open":false}',clock_timestamp())`, f.person.TenantID, uid(), f.project, f.ticket, repository, branch); err != nil {
						return err
					}
					_, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_pulls(tenant_id,repository,pull_request,head_branch,opened_at,source,recorded_at) VALUES($1,$2,42,$3,clock_timestamp(),'webhook',clock_timestamp())`, f.person.TenantID, repository, branch)
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO delivery_items(tenant_id,id,project_id,ticket_node_id,repository,branch,head_sha,state,state_since,owner,observation,updated_at) VALUES($1,$2,$3,$4,$5,$6,repeat('b',40),'built',clock_timestamp(),'coordinator','{"open":false}',clock_timestamp())`, f.person.TenantID, uid(), f.project, f.ticket, repository, branch)
				return err
			})
			for _, wrong := range []any{nil, "", "work/hts-2-other", "Work/hts-2-built"} {
				body := buildRegistration(f, "wrong branch")
				body["branch"] = wrong
				buildRefusal(t, f.call(f.agent, "POST", base, body, ""), "built_unmerged")
			}
			// A managed order pins its repository: the same name in another
			// repository is not a continuation of this project's delivery.
			managed := managedBuildRegistration(t, f)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=jsonb_set(fields,'{build_delivery}','{"repository":"owner/unknown","branch":"work/hts-2-built"}') WHERE id=$1`, managed["work_order_id"])
				return err
			})
			managed["branch"] = branch
			buildRefusal(t, f.call(f.agent, "POST", base, managed, ""), "built_unmerged")
			orderOnly := buildRegistration(f, "ticket-only order binding")
			orderOnly["ticket_node_id"], orderOnly["branch"] = managed["work_order_id"], branch
			buildRefusal(t, f.call(f.agent, "POST", base, orderOnly, ""), "built_unmerged")
			body := buildRegistration(f, "fix round")
			body["branch"] = branch
			accepted := f.call(f.agent, "POST", base, body, "")
			expect(t, accepted, 201)
			s := decode(t, accepted)
			continuation, ok := s["build_continuation"].(map[string]any)
			if !ok || continuation["repository"] != repository || continuation["branch"] != branch || s["parallel_reason"] != nil {
				t.Fatalf("missing continuation metadata or invented parallel reason: %s", accepted.Body)
			}
			message := "continues delivery on " + branch
			if source == "pull" {
				message = "continues delivery PR #42 on " + branch
				if continuation["pull_request"] != float64(42) {
					t.Fatal("continuation lost its pull request")
				}
			}
			expect(t, f.call(f.agent, "POST", base, body, ""), 201)
			for _, next := range []any{branch, "work/hts-2-other", nil} {
				blocked := buildRegistration(f, "overlapping round")
				blocked["branch"] = next
				buildRefusal(t, f.call(f.agent, "POST", base, blocked, ""), "active_builder")
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var audits, comments, wrongTarget int
				err := tx.QueryRow(t.Context(), `SELECT
				 (SELECT count(*) FROM events WHERE node_id=$1 AND type='delivery.continued' AND actor_principal_id=$2 AND after->>'session_id'=$3 AND after->>'message'=$4),
				 (SELECT count(*) FROM events WHERE node_id=$1 AND type='comment.created' AND after->>'body_markdown'=$4),
				 (SELECT count(*) FROM events WHERE node_id=$5 AND type='delivery.continued')`, f.ticket, f.agent.ID, s["id"], message, managed["work_order_id"]).Scan(&audits, &comments, &wrongTarget)
				if err == nil && (audits != 1 || comments != 1 || wrongTarget != 0) {
					t.Errorf("continuation audit/replay: audit=%d comments=%d wrong_target=%d", audits, comments, wrongTarget)
				}
				return err
			})
			// Rebinding uses the current branch and the same predicate.
			unbound := buildRegistration(f, "rebound fix")
			delete(unbound, "ticket_node_id")
			delete(unbound, "work_shape")
			unbound["branch"] = branch
			w := f.call(f.agent, "POST", base, unbound, "")
			expect(t, w, 201)
			rebound := decode(t, w)
			path := base + "/" + rebound["id"].(string)
			binding := map[string]any{"expected_revision": rebound["revision"], "ticket_node_id": f.ticket, "work_shape": "ship"}
			buildRefusal(t, f.call(f.agent, "PATCH", path+"/binding", binding, ""), "active_builder")
			expect(t, f.call(f.agent, "POST", base+"/"+s["id"].(string)+"/stop", map[string]any{"reason": "process_exited"}, body["worker_lease"].(string)), 200)
			w = f.call(f.agent, "PATCH", path+"/binding", binding, "")
			expect(t, w, 200)
			if decode(t, w)["build_continuation"] == nil {
				t.Fatal("rebind omitted continuation metadata")
			}
		})
	}
}

// Risk: direct store callers can bypass repository checks or append an audit
// before failed work commits. Rollback must discard both continuation events.
func TestDeliveryContinuationStoreBindingAndRollback(t *testing.T) {
	f := fixture(t)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository,ci_workflow,nightly_workflow,updated_at) VALUES($1,$2,'owner/linked','CI','Nightly',clock_timestamp())`, f.person.TenantID, f.project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_pulls(tenant_id,repository,pull_request,head_branch,opened_at,source,recorded_at) VALUES($1,'owner/linked',42,'work/hts-2-built',clock_timestamp(),'webhook',clock_timestamp())`, f.person.TenantID)
		return err
	})
	for _, repository := range []string{"owner/linked", "owner/unknown"} {
		ctx := tenant.WithPrincipal(t.Context(), f.person)
		rollback := errors.New("rollback continuation")
		err := db.InTenant(ctx, f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
			if err := db.LockWorkTreeTx(ctx, tx); err != nil {
				return err
			}
			candidate := &workorders.BuildCandidate{Repository: repository, Branch: "work/hts-2-built"}
			order, _, err := workorders.CreateDeferred(ctx, tx, f.person, workorders.CreateInput{Title: "Fix", Parent: &f.ticket, Criteria: []string{"Continue delivery"}, BuildDelivery: candidate})
			if err != nil {
				return err
			}
			admission, err := workorders.OrderBuildAdmissionTx(ctx, tx, order.NodeID)
			if err != nil || admission.Candidate == nil || *admission.Candidate != *candidate || order.BuildDelivery == nil || *order.BuildDelivery != *candidate {
				t.Fatalf("order lost its repository/branch binding: %+v, %v", admission, err)
			}
			err = workorders.RequireBuildAdmissionTx(ctx, tx, f.person, order.NodeID, "", nil, &admission)
			if repository != "owner/linked" {
				var conflict *workorders.BuildConflict
				if !errors.As(err, &conflict) || conflict.Reason != "built_unmerged" || admission.Continuation != nil {
					t.Fatalf("unknown repository was admitted: %+v, %v", admission, err)
				}
				return rollback
			}
			if err != nil || admission.Continuation == nil {
				t.Fatalf("same-branch store admission: %+v, %v", admission, err)
			}
			if err = admission.Record(ctx, tx, f.person, "store-session", ""); err != nil {
				return err
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatal(err)
		}
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE node_id=$1 AND (type='delivery.continued' OR after->>'body_markdown' LIKE 'continues delivery%')`, f.ticket).Scan(&count)
		if err == nil && count != 0 {
			t.Fatal("rollback left a continuation audit")
		}
		return err
	})
}

func queueableBuildTicket(t *testing.T, f *harnessFixture) {
	t.Helper()
	agentruns.New(f.db.App).Mount(f.mux)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='open',fields='{"estimate_hours":2,"acceptance_criteria":["Ship the result"]}' WHERE id=$1`, f.ticket)
		return err
	})
}

func buildRefusal(t *testing.T, w *httptest.ResponseRecorder, reason string) map[string]any {
	t.Helper()
	expect(t, w, http.StatusConflict)
	body := decode(t, w)
	if body["code"] != "build_in_flight" || body["reason"] != reason || body["since"] == nil {
		t.Fatalf("wrong builder refusal: %s", w.Body)
	}
	return body
}

func managedBuildRegistration(t *testing.T, f *harnessFixture) map[string]any {
	t.Helper()
	order, run := uid(), uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title,parent_id)
		 SELECT $1,$2,id,'HTS-3','Managed build',$3 FROM node_kinds WHERE slug='work_order'`, f.person.TenantID, order, f.ticket); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,status) VALUES($1,$2,$3,'ready')`, f.person.TenantID, order, f.person.ID); err != nil {
			return err
		}
		// A settled run retains its real dispatch binding without making the
		// queue busy independently of the worker being tested.
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,id,work_order_id,agent_principal_id,status,queue_node_id,queue_by_principal_id,queue_at,queue_security_review_required) VALUES($1,$2,$3,$4,'completed',$5,$6,clock_timestamp(),false)`, f.person.TenantID, run, order, f.agent.ID, f.ticket, f.person.ID)
		return err
	})
	body := buildRegistration(f, "managed builder")
	body["management_mode"], body["ticket_node_id"] = "managed", order
	body["work_order_id"], body["run_id"] = order, run
	return body
}

// Risk: the daemon binds to a work order while direct clients and queueing
// bind to its originating ticket. All supported bindings must share admission.
func TestManagedTicketBuilderAdmissionSharesBindingsAndQueue(t *testing.T) {
	for _, binding := range []string{"managed-first", "direct-first", "order-only", "run-only", "race"} {
		t.Run(binding, func(t *testing.T) {
			f := fixture(t)
			base := "/api/projects/" + f.project + "/harness-sessions"
			queueableBuildTicket(t, f)
			managed, direct := managedBuildRegistration(t, f), buildRegistration(f, "direct builder")
			var first *httptest.ResponseRecorder
			switch binding {
			case "direct-first":
				first = f.call(f.person, "POST", base, direct, "")
				expect(t, first, 201)
				buildRefusal(t, f.call(f.agent, "POST", base, managed, ""), "active_builder")
			case "race":
				pool, barrier, ctx := dbtest.BarrierPool(t, f.db.App, func(sql string) bool { return strings.Contains(sql, "aeon-builder:") })
				f.mux = http.NewServeMux()
				harness.New(pool).Mount(f.mux)
				agentruns.New(f.db.App).Mount(f.mux)
				firstDone, secondDone := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
				go func() { firstDone <- f.call(f.agent, "POST", base, managed, "") }()
				holder := barrier.Wait(t, ctx)
				finished := make(chan struct{})
				go func() {
					secondDone <- f.call(f.person, "POST", base, direct, "")
					close(finished)
				}()
				if dbtest.BlockedOrDone(t, ctx, f.db.Admin, holder, finished) == "" {
					t.Error("direct registration escaped the managed admission transaction")
				}
				barrier.Release()
				first = dbtest.Await(t, ctx, firstDone)
				expect(t, first, 201)
				buildRefusal(t, dbtest.Await(t, ctx, secondDone), "active_builder")
			default:
				if binding != "managed-first" {
					delete(managed, "ticket_node_id")
					delete(managed, "work_shape")
					if binding == "order-only" {
						delete(managed, "run_id")
					}
				}
				first = f.call(f.agent, "POST", base, managed, "")
				expect(t, first, 201)
				if binding == "run-only" {
					// Legacy rows may retain only the run reference.
					f.tx(t, f.person, func(tx pgx.Tx) error {
						_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET work_order_id=NULL WHERE id=$1`, decode(t, first)["id"])
						return err
					})
				}
				buildRefusal(t, f.call(f.person, "POST", base, direct, ""), "active_builder")
			}
			refusal := buildRefusal(t, f.call(f.person, "POST", "/api/queue", map[string]any{"node_id": f.ticket}, ""), "active_builder")
			if refusal["session_id"] != decode(t, first)["id"] {
				t.Fatal("queue did not identify the actual blocking generation")
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var active, runs int
				err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM harness_sessions WHERE role='worker' AND stopped_at IS NULL), (SELECT count(*) FROM agent_runs)`).Scan(&active, &runs)
				if err == nil && (active != 1 || runs != 1) {
					t.Errorf("refused admission wrote extra work: active=%d runs=%d", active, runs)
				}
				return err
			})
		})
	}
}

// Risks: managed workers miss delivery facts and audit their exception on the
// order instead of the originating ticket seen by people and queue clients.
func TestManagedBuilderDeliveryAndParallelAuditUseOriginTicket(t *testing.T) {
	for _, scenario := range []string{"delivery", "parallel-audit"} {
		t.Run(scenario, func(t *testing.T) {
			f := fixture(t)
			base := "/api/projects/" + f.project + "/harness-sessions"
			managed := managedBuildRegistration(t, f)
			if scenario == "delivery" {
				f.tx(t, f.person, func(tx pgx.Tx) error {
					if _, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository,ci_workflow,nightly_workflow,updated_at) VALUES($1,$2,'owner/managed','CI','Nightly',clock_timestamp())`, f.person.TenantID, f.project); err != nil {
						return err
					}
					_, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_pulls(tenant_id,repository,pull_request,head_branch,opened_at,source,recorded_at) VALUES($1,'owner/managed',72,'work/hts-2-built',clock_timestamp(),'webhook',clock_timestamp())`, f.person.TenantID)
					return err
				})
				buildRefusal(t, f.call(f.agent, "POST", base, managed, ""), "built_unmerged")
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE delivery_metric_pulls SET closed_at=clock_timestamp(),merged_at=clock_timestamp() WHERE repository='owner/managed' AND pull_request=72`)
					return err
				})
				expect(t, f.call(f.agent, "POST", base, managed, ""), 201)
				return
			}
			expect(t, f.call(f.agent, "POST", base, buildRegistration(f, "direct builder"), ""), 201)
			managed["parallel_reason"] = "Independent managed comparison"
			expect(t, f.call(f.agent, "POST", base, managed, ""), 403)
			accepted := f.call(f.person, "POST", base, managed, "")
			expect(t, accepted, 201)
			expect(t, f.call(f.person, "POST", base, managed, ""), 201)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var audits, comments, wrongTarget int
				err := tx.QueryRow(t.Context(), `SELECT
				 (SELECT count(*) FROM events WHERE node_id=$1 AND type='harness.parallel_build_started' AND actor_principal_id=$2 AND after->>'session_id'=$3 AND after->>'parallel_reason'='Independent managed comparison'),
				 (SELECT count(*) FROM events WHERE node_id=$1 AND type='comment.created' AND after->>'body_markdown' LIKE 'Parallel work authorized for session %'),
				 (SELECT count(*) FROM events WHERE node_id=$4 AND type IN ('harness.parallel_build_started','comment.created'))`, f.ticket, f.person.ID, decode(t, accepted)["id"], managed["work_order_id"]).Scan(&audits, &comments, &wrongTarget)
				if err == nil && (audits != 1 || comments != 1 || wrongTarget != 0) {
					t.Errorf("managed parallel audit target/replay: audits=%d comments=%d order_events=%d", audits, comments, wrongTarget)
				}
				return err
			})
		})
	}
}

// Risks: duplicate builds under RLS and an alternate binding/revival entry
// bypassing exclusion. A PostgreSQL barrier establishes overlapping requests;
// the observed lock wait, rather than scheduler timing, proves the race.
func TestTicketBuilderAdmissionRaceBindingRevivalAndQueue(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	pool, barrier, ctx := dbtest.BarrierPool(t, f.db.App, func(sql string) bool {
		return strings.Contains(sql, "aeon-builder:")
	})
	f.mux = http.NewServeMux()
	harness.New(pool).Mount(f.mux)
	queueableBuildTicket(t, f)
	firstBody, secondBody := buildRegistration(f, "first builder"), buildRegistration(f, "second builder")
	firstDone, secondDone := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- f.call(f.person, "POST", base, firstBody, "") }()
	holder := barrier.Wait(t, ctx)
	finished := make(chan struct{})
	go func() {
		secondDone <- f.call(f.agent, "POST", base, secondBody, "")
		close(finished)
	}()
	if lock := dbtest.BlockedOrDone(t, ctx, f.db.Admin, holder, finished); lock == "" {
		t.Error("competing registration completed while the first held admission")
	}
	barrier.Release()
	first := dbtest.Await(t, ctx, firstDone)
	expect(t, first, 201)
	session := decode(t, first)
	id := session["id"].(string)
	refusal := buildRefusal(t, dbtest.Await(t, ctx, secondDone), "active_builder")
	starter := refusal["started_by"].(map[string]any)
	if refusal["session_id"] != id || refusal["label"] != "first builder" || starter["id"] != f.person.ID || starter["kind"] != "person" || refusal["since"] != session["created_at"] {
		t.Fatalf("refusal lost the blocking generation or initiator: %+v", refusal)
	}
	// Replay is still the same generation and emits no second registration.
	replay := f.call(f.person, "POST", base, firstBody, "")
	expect(t, replay, 201)
	if decode(t, replay)["id"] != id {
		t.Fatal("exact replay opened another builder")
	}
	buildRefusal(t, f.call(f.person, "POST", "/api/queue", map[string]any{"node_id": f.ticket}, ""), "active_builder")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var active, orders, runs, registered int
		err := tx.QueryRow(t.Context(), `SELECT
		 (SELECT count(*) FROM harness_sessions WHERE ticket_node_id=$1 AND stopped_at IS NULL AND role='worker'),
		 (SELECT count(*) FROM work_orders),(SELECT count(*) FROM agent_runs),
		 (SELECT count(*) FROM events WHERE type='harness.registered')`, f.ticket).Scan(&active, &orders, &runs, &registered)
		if err == nil && (active != 1 || orders != 0 || runs != 0 || registered != 1) {
			t.Errorf("race/rejected queue wrote extra work: active=%d orders=%d runs=%d events=%d", active, orders, runs, registered)
		}
		return err
	})
	// A previously unbound worker cannot enter the occupied ticket via PATCH.
	unboundBody := buildRegistration(f, "unbound")
	delete(unboundBody, "ticket_node_id")
	delete(unboundBody, "work_shape")
	unbound := f.call(f.person, "POST", base, unboundBody, "")
	expect(t, unbound, 201)
	unboundSession := decode(t, unbound)
	unboundPath := base + "/" + unboundSession["id"].(string)
	buildRefusal(t, f.call(f.person, "PATCH", unboundPath+"/binding", map[string]any{
		"expected_revision": unboundSession["revision"], "ticket_node_id": f.ticket, "work_shape": "ship", "parent_harness_session_id": nil,
	}, ""), "active_builder")
	check := f.call(f.person, "GET", unboundPath, nil, "")
	expect(t, check, 200)
	if decode(t, check)["ticket_node_id"] != nil {
		t.Fatal("refused binding changed the record")
	}
	// Lost contact frees registration, but revival may not displace its successor.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp(),stop_reason='heartbeat_lost' WHERE id=$1`, id)
		return err
	})
	replacement := f.call(f.agent, "POST", base, secondBody, "")
	expect(t, replacement, 201)
	replacementID := decode(t, replacement)["id"].(string)
	expect(t, f.call(f.agent, "POST", base+"/"+id+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1}, firstBody["worker_lease"].(string)), 403)
	expect(t, f.call(f.agent, "POST", base+"/"+replacementID+"/stop", map[string]any{"reason": "process_exited"}, secondBody["worker_lease"].(string)), 200)
	afterStop := f.call(f.agent, "POST", base, buildRegistration(f, "after stop"), "")
	expect(t, afterStop, 201)
	foreign := f.call(f.foreign, "POST", base, buildRegistration(f, "foreign"), "")
	expect(t, foreign, 404)
	if strings.Contains(foreign.Body.String(), id) || strings.Contains(foreign.Body.String(), "first builder") {
		t.Fatal("another tenant learned the blocking session")
	}
	// An unavailable legacy initiator must never make its session disappear.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET started_by_principal_id=$2 WHERE id=$1`, decode(t, afterStop)["id"], uid())
		return err
	})
	missingActor := buildRefusal(t, f.call(f.agent, "POST", base, buildRegistration(f, "missing actor"), ""), "active_builder")
	if missingActor["started_by"] != nil || missingActor["session_id"] != decode(t, afterStop)["id"] {
		t.Fatal("missing actor metadata bypassed exclusion or invented an initiator")
	}
	// Project-only RLS cannot hide a blocker after the ticket changes projects.
	destination := uid()
	f.addNode(t, destination, "GUARD-3", "project", "", "Destination project")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.ticket, destination)
		return err
	})
	limited := db.OnlyProjects(tenant.WithPrincipal(t.Context(), f.person), destination)
	if err := db.InTenant(limited, f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
		if err := db.LockWorkTreeTx(limited, tx); err != nil {
			return err
		}
		if err := authz.RequireTx(limited, tx, f.person, "harness.write", authz.Scope{ProjectID: destination}); err != nil {
			return err
		}
		err := workqueue.RequireBuildAdmissionTx(limited, tx, f.person, f.ticket, "", nil)
		var conflict *workqueue.BuildConflict
		if !errors.As(err, &conflict) || conflict.Reason != "active_builder" {
			t.Fatalf("hidden old-project builder did not block admission: %v", err)
		}
		if conflict.SessionID != nil || conflict.Label != nil || conflict.StartedBy != nil || conflict.Since != nil {
			t.Fatal("hidden blocker metadata escaped internal existence probe")
		}
		var visible int
		if err := tx.QueryRow(limited, `SELECT count(*) FROM harness_sessions WHERE project_id=$1`, f.project).Scan(&visible); err != nil {
			return err
		}
		if visible != 0 {
			t.Fatal("internal probe left caller project visibility widened")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Risks: workers self-grant parallel authority, stale key ceilings outlive a
// revocation, and parallel work is invisible or duplicated in ticket history.
func TestTicketParallelBuildRequiresCurrentAuthorityAndAudit(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	expect(t, f.call(f.agent, "POST", base, buildRegistration(f, "primary"), ""), 201)
	parallel := buildRegistration(f, "independent researcher")
	parallel["parallel_reason"] = "  Independent research for comparison  "
	parallel["work_shape"] = "scout"
	parallel["advertised_capabilities"] = []string{"pause"}
	expect(t, f.call(f.agent, "POST", base, parallel, ""), 403)
	coordinator := buildRegistration(f, "declared coordinator")
	coordinator["role"] = "coordinator"
	delete(coordinator, "ticket_node_id")
	delete(coordinator, "work_shape")
	parent := f.call(f.agent, "POST", base, coordinator, "")
	expect(t, parent, 201)
	parallel["parent_harness_session_id"] = decode(t, parent)["id"]
	expect(t, f.call(f.agent, "POST", base, parallel, ""), 403)
	for _, reason := range []string{"   ", "line\nbreak", "line\u2028break", strings.Repeat("x", 501)} {
		bad := buildRegistration(f, "invalid reason")
		bad["parallel_reason"] = reason
		expect(t, f.call(f.person, "POST", base, bad, ""), 400)
	}
	accepted := f.call(f.person, "POST", base, parallel, "")
	expect(t, accepted, 201)
	personSession := decode(t, accepted)
	if personSession["parallel_reason"] != "Independent research for comparison" || personSession["started_by_principal_id"] != f.person.ID {
		t.Fatal("parallel reason and person initiator were not persisted")
	}
	expect(t, f.call(f.person, "POST", base, parallel, ""), 201)
	parallel["parallel_reason"] = "different reason"
	expect(t, f.call(f.person, "POST", base, parallel, ""), 409)
	// Coordinator authority comes from current grants and the exact bearer key,
	// not the request role, parent, or middleware's cached scope list.
	dbtest.BindRole(t, f.db, f.agent.TenantID, f.agent.ID, "coordinator")
	scopes := append(slices.Clone(authz.CoordinatorKeyScopes), "harness.worker")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=$2 WHERE principal_id=$1`, f.agent.ID, scopes)
		return err
	})
	agentParallel := buildRegistration(f, "coordinator-authorized researcher")
	agentParallel["parallel_reason"] = "Independent coordinator comparison"
	expect(t, f.call(f.agent, "POST", base, agentParallel, ""), 201)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['harness.write','harness.worker'] WHERE principal_id=$1`, f.agent.ID)
		return err
	})
	f.agent.Scopes = scopes // Deliberately stale authenticated snapshot.
	denied := buildRegistration(f, "revoked ceiling")
	denied["parallel_reason"] = "Attempt after ceiling reduction"
	expect(t, f.call(f.agent, "POST", base, denied, ""), 403)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var audit, comments, active int
		err := tx.QueryRow(t.Context(), `SELECT
		 (SELECT count(*) FROM events WHERE node_id=$1 AND type='harness.parallel_build_started'
		 AND actor_principal_id IN ($2,$3) AND after->>'parallel_reason' IN ('Independent research for comparison','Independent coordinator comparison')),
		 (SELECT count(*) FROM events WHERE node_id=$1 AND type='comment.created' AND after->>'body_markdown' LIKE 'Parallel work authorized for session %'),
		 (SELECT count(*) FROM harness_sessions WHERE ticket_node_id=$1 AND stopped_at IS NULL AND role='worker')`, f.ticket, f.person.ID, f.agent.ID).Scan(&audit, &comments, &active)
		if err == nil && (audit != 2 || comments != 2 || active != 3) {
			t.Errorf("parallel audit or refusal was not atomic/idempotent: audit=%d comments=%d active=%d", audit, comments, active)
		}
		return err
	})
	// Resuming deliberately parallel work retains the recorded reason and
	// repeats current authority. The other workers remain active throughout.
	path := base + "/" + personSession["id"].(string)
	paused := f.call(f.person, "POST", path+"/pause", map[string]any{}, "")
	expect(t, paused, 200)
	finishPaused(t, f, path, parallel["worker_lease"].(string), decode(t, paused)["pause"].(map[string]any)["control_id"].(string))
	proof := map[string]any{"harness_session_ref": "parallel-resume-ref-" + uid(), "worker_lease": "parallel-resume-lease-" + uid()}
	resumed := f.call(f.person, "POST", path+"/resume", map[string]any{"registration": proof}, "")
	expect(t, resumed, 200)
	successor := decode(t, resumed)["successor"].(map[string]any)
	if successor["parallel_reason"] != personSession["parallel_reason"] || successor["id"] == personSession["id"] {
		t.Fatal("parallel continuation lost its audited reason or reused the stopped generation")
	}
}

// Risks: rebuilding already-built work, mismatching 2 with 20, cross-repository
// evidence, and stale delivery projections continuing to block after a merge.
func TestTicketBuiltUnmergedAdmissionUsesLinkedDeliveryAndMerge(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	queueableBuildTicket(t, f)
	const repository = "owner/linked"
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository,ci_workflow,nightly_workflow,updated_at) VALUES($1,$2,$3,'CI','Nightly',clock_timestamp())`, f.person.TenantID, f.project, repository); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_pulls(tenant_id,repository,pull_request,head_branch,opened_at,source,recorded_at) VALUES($1,$2,42,'work/hts-2-built',clock_timestamp(),'webhook',clock_timestamp())`, f.person.TenantID, repository)
		return err
	})
	buildRefusal(t, f.call(f.agent, "POST", base, buildRegistration(f, "duplicate build"), ""), "built_unmerged")
	queueRefusal := f.call(f.person, "POST", "/api/queue", map[string]any{"node_id": f.ticket}, "")
	buildRefusal(t, queueRefusal, "built_unmerged")
	if !strings.Contains(queueRefusal.Body.String(), "already built — ship it") {
		t.Fatal("queue did not explain that existing work needs shipping")
	}
	assertAllowed := func(label string) {
		t.Helper()
		body := buildRegistration(f, label)
		w := f.call(f.agent, "POST", base, body, "")
		expect(t, w, 201)
		id := decode(t, w)["id"].(string)
		expect(t, f.call(f.agent, "POST", base+"/"+id+"/stop", map[string]any{"reason": "process_exited"}, body["worker_lease"].(string)), 200)
	}
	// Merge facts immediately supersede an older projected PR row.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE delivery_metric_pulls SET merged_at=clock_timestamp(),closed_at=clock_timestamp() WHERE pull_request=42`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_items(tenant_id,id,project_id,ticket_node_id,repository,pull_request,branch,head_sha,state,state_since,owner,observation,updated_at)
		 VALUES($1,$2,$3,$4,$5,42,'work/hts-2-built',repeat('a',40),'built',clock_timestamp(),'coordinator','{"open":true}',clock_timestamp())`, f.person.TenantID, uid(), f.project, f.ticket, repository)
		return err
	})
	assertAllowed("after merge")
	var branchID = uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_items(tenant_id,id,project_id,ticket_node_id,repository,branch,head_sha,state,state_since,owner,observation,updated_at)
		 VALUES($1,$2,$3,$4,$5,'work/hts-2-wip',repeat('b',40),'built',clock_timestamp(),'coordinator','{"open":false}',clock_timestamp())`, f.person.TenantID, branchID, f.project, f.ticket, repository)
		return err
	})
	refusal := buildRefusal(t, f.call(f.person, "POST", "/api/queue", map[string]any{"node_id": f.ticket}, ""), "built_unmerged")
	if refusal["delivery"].(map[string]any)["branch"] != "work/hts-2-wip" {
		t.Fatal("unmerged local branch evidence was not identified")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE delivery_items SET branch='work/hts-20-other' WHERE id=$1`, branchID)
		return err
	})
	assertAllowed("another ticket key")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE delivery_items SET branch='work/hts-2-wip' WHERE id=$1`, branchID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE delivery_metric_sources SET repository='owner/other' WHERE project_id=$1`, f.project)
		return err
	})
	assertAllowed("another repository")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM delivery_metric_sources WHERE project_id=$1`, f.project)
		return err
	})
	assertAllowed("no linked repository")
	// Refusals created no queue work; after merge/unlink the normal queue remains usable.
	w := f.call(f.person, "POST", "/api/queue", map[string]any{"node_id": f.ticket}, "")
	expect(t, w, 200)
	queuedBuilder := buildRegistration(f, "started after queueing")
	active := f.call(f.agent, "POST", base, queuedBuilder, "")
	expect(t, active, 201)
	next := f.call(f.person, "POST", "/api/queue/next", map[string]any{}, "")
	expect(t, next, 200)
	if result := decode(t, next); result["entry"] != nil || result["wait_reason"] != "build_in_flight" {
		t.Fatalf("queue pickup ignored an intervening builder: %s", next.Body)
	}
	expect(t, f.call(f.agent, "POST", base+"/"+decode(t, active)["id"].(string)+"/stop", map[string]any{"reason": "process_exited"}, queuedBuilder["worker_lease"].(string)), 200)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository,ci_workflow,nightly_workflow,updated_at) VALUES($1,$2,$3,'CI','Nightly',clock_timestamp())`, f.person.TenantID, f.project, repository)
		return err
	})
	next = f.call(f.person, "POST", "/api/queue/next", map[string]any{}, "")
	expect(t, next, 200)
	if result := decode(t, next); result["entry"] != nil || result["wait_reason"] != "already_built_ship_it" {
		t.Fatalf("queue pickup ignored intervening delivery evidence: %s", next.Body)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var orders, runs int
		err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM work_orders),(SELECT count(*) FROM agent_runs)`).Scan(&orders, &runs)
		if err == nil && (orders != 1 || runs != 1) {
			t.Errorf("refused queues created work: orders=%d runs=%d", orders, runs)
		}
		return err
	})
}

// Risk: read-only work becomes invisible behind a delivery/build conflict, or
// caller-supplied scout metadata substitutes for authenticated review authority.
func TestReadOnlyTicketAdmissionAndReviewerIdentity(t *testing.T) {
	for _, blocker := range []string{"delivery", "builder", "legacy-scout-writer"} {
		t.Run(blocker, func(t *testing.T) {
			f := fixture(t)
			nodes.New(f.db.App, nil).Mount(f.mux)
			base := "/api/projects/" + f.project + "/harness-sessions"
			author := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Agent}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'agent','Author')`, author.TenantID, author.ID)
				return err
			})
			dbtest.BindRole(t, f.db, author.TenantID, author.ID, "member")
			body := buildRegistration(f, "author")
			body["agent_principal_id"], body["model"] = author.ID, "gpt-6.1-sol"
			w := f.call(f.person, "POST", base, body, "")
			expect(t, w, 201)
			authorID := decode(t, w)["id"].(string)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				if blocker == "delivery" {
					if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped',stop_reason='process_exited' WHERE id=$1`, authorID); err != nil {
						return err
					}
				}
				if blocker == "legacy-scout-writer" {
					if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET work_shape='scout',branch='work/hts-2-built' WHERE id=$1`, authorID); err != nil {
						return err
					}
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository,ci_workflow,nightly_workflow,updated_at) VALUES($1,$2,'owner/linked','CI','Nightly',clock_timestamp())`, f.person.TenantID, f.project); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=array_append(scopes,'nodes.read') WHERE id=$1`, f.agent.AuthKeyID); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO delivery_items(tenant_id,id,project_id,ticket_node_id,repository,pull_request,branch,head_sha,state,state_since,owner,observation,updated_at)
				 VALUES($1,$2,$3,$4,'owner/linked',42,'work/hts-2-built',repeat('b',40),'built',clock_timestamp(),'fixture',jsonb_build_object('open',true,'base_sha',repeat('a',40),'target_branch','main'),clock_timestamp())`, f.person.TenantID, uid(), f.project, f.ticket)
				return err
			})
			// A reviewer has its own narrow role as well as a narrow key. The
			// broad fixture member role intentionally counts as coordinator
			// authority and must not be able to claim review credentials.
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var role string
				if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'read_only_reviewer','Read-only reviewer') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest(ARRAY['nodes.read','harness.read','harness.write','harness.worker'])`, f.person.TenantID, role); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1`, f.agent.ID, role)
				return err
			})
			f.agent.KeyID = f.agent.AuthKeyID
			f.agent.KeyCreatorID = f.person.ID
			f.agent.Scopes = []string{"harness.worker", "harness.write", "nodes.read"}
			var reviewerID, reviewerLease string
			for _, label := range []string{"scout", "reviewer"} {
				readOnly := buildRegistration(f, label)
				readOnly["work_shape"] = "scout"
				readOnly["harness"], readOnly["model"], readOnly["reasoning_effort"] = "claude", "claude-opus-4-6", "xhigh"
				w := f.call(f.agent, "POST", base, readOnly, "")
				expect(t, w, 201)
				session := decode(t, w)
				id := session["id"].(string)
				if session["ticket_node_id"] != f.ticket || session["work_shape"] != "scout" || session["build_continuation"] != nil || session["parallel_reason"] != nil {
					t.Fatal("read-only registration lost ticket or invented builder authority")
				}
				read := f.call(f.person, "GET", base+"/"+id, nil, "")
				expect(t, read, 200)
				if decode(t, read)["ticket_node_id"] != f.ticket {
					t.Fatal("existing session read lost ticket visibility")
				}
				expect(t, f.call(f.agent, "POST", base, readOnly, ""), 201)
				f.tx(t, f.person, func(tx pgx.Tx) error {
					var registered, admitted int
					err := tx.QueryRow(t.Context(), `SELECT
					 (SELECT count(*) FROM events WHERE type='harness.registered' AND after->>'id'=$1),
					 (SELECT count(*) FROM events WHERE node_id=$2 AND type='harness.read_only_admitted' AND actor_principal_id=$3 AND after->>'session_id'=$1 AND after->>'work_shape'='scout' AND after->>'authority_granted'='false')`, id, f.ticket, f.agent.ID).Scan(&registered, &admitted)
					if err == nil && (registered != 1 || admitted != 1) {
						t.Errorf("read-only admission/replay audit: registered=%d admitted=%d", registered, admitted)
					}
					return err
				})
				if label == "reviewer" {
					reviewerID, reviewerLease = id, readOnly["worker_lease"].(string)
				}
			}
			// The dispatcher binds the normally registered reviewer to the exact
			// delivery through AEON-1391. No reviewer role/second authority exists.
			create := reviewgate.RunCreate{Repository: "owner/linked", PR: 42, Target: "main", Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40), AuthorSession: authorID, ReviewerSession: reviewerID, Kind: "review"}
			create.Scope = create.Base + ".." + create.Head
			w = f.call(f.person, "POST", "/api/nodes/"+f.ticket+"/review-runs", create, "")
			expect(t, w, 201)
			var run reviewgate.Run
			if json.Unmarshal(w.Body.Bytes(), &run) != nil || run.ReviewerSession != reviewerID || run.Reviewer != f.agent.ID || run.ReviewerFamily != "anthropic" {
				t.Fatal("dispatcher did not bind the registered reviewer identity")
			}
			claimPath := "/api/review-runs/" + run.ID + "/credential"
			expect(t, f.call(f.person, "POST", claimPath, nil, ""), 403)
			expect(t, f.call(author, "POST", claimPath, nil, ""), 403)
			verdict := reviewgate.RunSubmit{Binding: run.RunBinding, Verdict: "ok", VisualImpact: "no", Reason: "Fixture review"}
			expect(t, f.call(f.agent, "POST", "/api/review-runs/"+run.ID+"/verdict", verdict, ""), 403)
			claim := f.call(f.agent, "POST", claimPath, nil, "")
			if claim.Code != 200 {
				t.Fatalf("bound reviewer claim: status %d (response omitted)", claim.Code)
			}
			capability, ok := decode(t, claim)["credential"].(string)
			if !ok || capability == "" {
				t.Fatal("reviewer claim omitted credential")
			}
			submit := func(id string, input reviewgate.RunSubmit, status int) {
				t.Helper()
				raw, _ := json.Marshal(input)
				r := httptest.NewRequest("POST", "/api/review-runs/"+id+"/verdict", strings.NewReader(string(raw)))
				r.Header.Set(reviewgate.CredentialHeader, capability)
				w := httptest.NewRecorder()
				f.mux.ServeHTTP(w, r)
				if w.Code != status {
					t.Fatalf("review submission: status %d, want %d (response omitted)", w.Code, status)
				}
			}
			w = f.call(f.person, "POST", "/api/nodes/"+f.ticket+"/review-runs", create, "")
			expect(t, w, 201)
			submit(decode(t, w)["id"].(string), verdict, 401)
			wrong := verdict
			wrong.Binding.Head = strings.Repeat("c", 40)
			wrong.Binding.Scope = wrong.Binding.Base + ".." + wrong.Binding.Head
			submit(run.ID, wrong, 409)
			// A run-bound reviewer cannot detach/rebind the session to impersonate
			// another ticket. Failure preserves the original session and audit.
			otherTicket := uid()
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title,parent_id) SELECT $1,$2,kind_id,'HTS-4','Other ticket',$3 FROM nodes WHERE id=$4`, f.person.TenantID, otherTicket, f.project, f.ticket)
				return err
			})
			snapshot := readOnlyState(t, f)
			binding := map[string]any{"expected_revision": 1, "ticket_node_id": otherTicket, "work_shape": "scout"}
			denied := f.call(f.agent, "PATCH", base+"/"+reviewerID+"/binding", binding, "")
			expect(t, denied, 409)
			if !strings.Contains(denied.Body.String(), "authenticated reviewer session") || readOnlyState(t, f) != snapshot {
				t.Fatal("rejected reviewer identity substitution mutated state or failed for another reason")
			}
			denied = f.call(f.agent, "POST", base+"/"+reviewerID+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "branch": "work/hts-2-built"}, reviewerLease)
			expect(t, denied, 409)
			if !strings.Contains(denied.Body.String(), "read-only session") || readOnlyState(t, f) != snapshot {
				t.Fatal("authenticated reviewer acquired delivery binding")
			}
			submit(run.ID, verdict, 200)
			reason := "built_unmerged"
			if blocker != "delivery" {
				reason = "active_builder"
			}
			buildRefusal(t, f.call(f.agent, "POST", base, buildRegistration(f, "still a builder"), ""), reason)
		})
	}
}

// Snapshot stored data, not computed heartbeat clocks or session projections.
func readOnlyState(t *testing.T, f *harnessFixture) string {
	t.Helper()
	var state string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT jsonb_build_object(
		 'sessions',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM harness_sessions s),
		 'deliveries',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM delivery_items d),
		 'reviews',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM review_runs r),
		 'events',(SELECT count(*) FROM events))::text`).Scan(&state)
	})
	return state
}

// Risk: a read-only generation sheds its restriction by detach/rebind, reports
// delivery commits, or resumes with builder metadata. Failed writes are atomic.
func TestReadOnlySessionLifecycleCannotBecomeBuilder(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	body := buildRegistration(f, "scout")
	body["work_shape"], body["advertised_capabilities"] = "scout", []string{"pause"}
	for _, field := range []string{"branch", "work_order_id", "run_id"} {
		bad := maps.Clone(body)
		bad[field] = uid()
		if field == "branch" {
			bad[field] = "work/hts-2-built"
		}
		before := readOnlyState(t, f)
		w := f.call(f.agent, "POST", base, bad, "")
		expect(t, w, 409)
		if !strings.Contains(w.Body.String(), "read-only session") || readOnlyState(t, f) != before {
			t.Fatalf("%s registration did not fail atomically for read-only binding", field)
		}
	}
	w := f.call(f.agent, "POST", base, body, "")
	expect(t, w, 201)
	session := decode(t, w)
	path, lease := base+"/"+session["id"].(string), body["worker_lease"].(string)
	for _, update := range []map[string]any{
		{"branch": "work/hts-2-built"},
		{"commits": []map[string]any{{"sha": strings.Repeat("c", 40), "subject": "Fabricated delivery commit"}}},
	} {
		update["phase"], update["activity"], update["activity_sequence"] = "working", "busy", 1
		before := readOnlyState(t, f)
		w := f.call(f.agent, "POST", path+"/heartbeat", update, lease)
		expect(t, w, 409)
		if !strings.Contains(w.Body.String(), "read-only session") || readOnlyState(t, f) != before {
			t.Fatal("rejected branch/commit heartbeat changed state or failed for another reason")
		}
	}
	for _, p := range []tenant.Principal{f.person, f.agent} {
		for _, shape := range []string{"ship", "unknown"} {
			binding := map[string]any{"expected_revision": session["revision"], "ticket_node_id": f.ticket, "work_shape": shape}
			if shape == "unknown" {
				binding["ticket_node_id"] = nil
			}
			before := readOnlyState(t, f)
			w := f.call(p, "PATCH", path+"/binding", binding, "")
			expect(t, w, 409)
			if !strings.Contains(w.Body.String(), "read-only session") || readOnlyState(t, f) != before {
				t.Fatal("rejected shape promotion/detach changed state")
			}
		}
	}
	// Read-only sessions do not occupy a builder slot even when first to arrive.
	expect(t, f.call(f.agent, "POST", base, buildRegistration(f, "builder after scout"), ""), 201)
	buildRefusal(t, f.call(f.agent, "POST", base, buildRegistration(f, "competing builder"), ""), "active_builder")
	unbound := buildRegistration(f, "new scout binding")
	delete(unbound, "ticket_node_id")
	delete(unbound, "work_shape")
	w = f.call(f.agent, "POST", base, unbound, "")
	expect(t, w, 201)
	unboundSession := decode(t, w)
	binding := map[string]any{"expected_revision": unboundSession["revision"], "ticket_node_id": f.ticket, "work_shape": "scout"}
	w = f.call(f.agent, "PATCH", base+"/"+unboundSession["id"].(string)+"/binding", binding, "")
	expect(t, w, 200)
	if decode(t, w)["work_shape"] != "scout" {
		t.Fatal("rebind did not retain read-only restriction")
	}
	// Ordinary heartbeats remain usable; only delivery writes are restricted.
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}, lease), 200)
	w = f.call(f.person, "POST", path+"/pause", map[string]any{}, "")
	expect(t, w, 200)
	finishPaused(t, f, path, lease, decode(t, w)["pause"].(map[string]any)["control_id"].(string))
	expect(t, f.call(f.person, "POST", path+"/resume", map[string]any{}, ""), 200)
	for _, change := range []map[string]any{{"work_shape": "ship"}, {"role": "coordinator"}, {"branch": "work/hts-2-built"}, {"work_order_id": uid()}} {
		successor := maps.Clone(body)
		successor["succeeds_session_id"] = session["id"]
		successor["harness_session_ref"], successor["worker_lease"] = "successor-ref-"+uid(), "successor-lease-"+uid()
		maps.Copy(successor, change)
		before := readOnlyState(t, f)
		w := f.call(f.person, "POST", base, successor, "")
		expect(t, w, 409)
		if readOnlyState(t, f) != before {
			t.Fatal("rejected resume changed predecessor, successor, review or audit")
		}
	}
	proof := map[string]any{"harness_session_ref": "readonly-resume-ref-" + uid(), "worker_lease": "readonly-resume-lease-" + uid()}
	w = f.call(f.person, "POST", path+"/resume", map[string]any{"registration": proof}, "")
	expect(t, w, 200)
	next := decode(t, w)["successor"].(map[string]any)
	if next["work_shape"] != "scout" || next["branch"] != nil || next["ticket_node_id"] != f.ticket {
		t.Fatal("resumed session lost read-only classification")
	}
	nextPath := base + "/" + next["id"].(string)
	expect(t, f.call(f.agent, "PATCH", nextPath+"/binding", map[string]any{"expected_revision": next["revision"], "ticket_node_id": f.ticket, "work_shape": "ship"}, ""), 409)
	expect(t, f.call(f.agent, "POST", nextPath+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "commits": []map[string]any{{"sha": strings.Repeat("c", 40), "subject": "Delivery"}}}, proof["worker_lease"].(string)), 409)
}
