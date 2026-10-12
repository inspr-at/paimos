// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/jackc/pgx/v5"
)

func buildRegistration(f *harnessFixture, label string) map[string]any {
	return map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "builder-host", "management_mode": "unmanaged",
		"role": "worker", "ticket_node_id": f.ticket, "work_shape": "ship", "display_label": label,
		"harness_session_ref": "build-reference-" + uid(), "worker_lease": "build-worker-lease-" + uid(),
	}
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
	buildRefusal(t, f.call(f.agent, "POST", base+"/"+id+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1}, firstBody["worker_lease"].(string)), "active_builder")
	expect(t, f.call(f.agent, "POST", base+"/"+replacementID+"/stop", map[string]any{"reason": "process_exited"}, secondBody["worker_lease"].(string)), 200)
	expect(t, f.call(f.agent, "POST", base, buildRegistration(f, "after stop"), ""), 201)
	foreign := f.call(f.foreign, "POST", base, buildRegistration(f, "foreign"), "")
	expect(t, foreign, 404)
	if strings.Contains(foreign.Body.String(), id) || strings.Contains(foreign.Body.String(), "first builder") {
		t.Fatal("another tenant learned the blocking session")
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
	expect(t, f.call(f.agent, "POST", base, parallel, ""), 403)
	coordinator := buildRegistration(f, "declared coordinator")
	coordinator["role"] = "coordinator"
	delete(coordinator, "ticket_node_id")
	delete(coordinator, "work_shape")
	parent := f.call(f.agent, "POST", base, coordinator, "")
	expect(t, parent, 201)
	parallel["parent_harness_session_id"] = decode(t, parent)["id"]
	expect(t, f.call(f.agent, "POST", base, parallel, ""), 403)
	for _, reason := range []string{"   ", "line\nbreak", strings.Repeat("x", 501)} {
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
