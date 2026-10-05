// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func leadFixture(t *testing.T, f *harnessFixture, role string) (string, string) {
	t.Helper()
	lease := "lead-decision-fixture-lease-" + uid()
	w := f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "private-build-host", "harness_session_ref": "lead-ref-" + uid(),
		"worker_lease": lease, "management_mode": "unmanaged", "role": role, "account_label": "private-account-name",
	}, "")
	expect(t, w, 201)
	return decode(t, w)["id"].(string), lease
}

func leadBody(f *harnessFixture) map[string]any {
	return map[string]any{"request_id": uid(), "ticket_node_id": f.ticket, "stage": "queue", "outcome": "selected",
		"reason_codes": []string{"oldest_eligible"}, "policy_source": "project", "policy_revision": 7, "attempt": 1, "gates": []any{}, "results": []any{}}
}

func leadEventCount(t *testing.T, f *harnessFixture) int {
	t.Helper()
	var count int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='node.lead_decision_recorded'`, f.person.TenantID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestLeadDecisionReplayVisibilityAndOwnership(t *testing.T) {
	f := fixture(t)
	session, lease := leadFixture(t, f, "coordinator")
	path := "/api/projects/" + f.project + "/harness-sessions/" + session + "/lead-decisions"
	list := "/api/projects/" + f.project + "/lead-decisions"
	body := leadBody(f)
	expect(t, f.call(f.person, "POST", path, body, lease), 403)
	expect(t, f.call(f.agent, "POST", path, body, "wrong-lease-000000000000000000000000"), 403)
	worker, workerLease := leadFixture(t, f, "worker")
	expect(t, f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+worker+"/lead-decisions", body, workerLease), 403)
	w := f.call(f.agent, "POST", path, body, lease)
	expect(t, w, 200)
	first := decode(t, w)["decision"].(map[string]any)
	if first["authority_granted"] != false || first["evidence"] != "coordinator_reported" || first["session_id"] != session {
		t.Fatal(w.Body.String())
	}
	for _, private := range []string{lease, "private-build-host", "private-account-name", "lead-ref-"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("private registration leaked")
		}
	}
	w = f.call(f.agent, "POST", path, body, lease)
	expect(t, w, 200)
	got := decode(t, w)
	if got["replayed"] != true || got["decision"].(map[string]any)["event_id"] != first["event_id"] || leadEventCount(t, f) != 1 {
		t.Fatal("replay appended or changed evidence")
	}
	body["policy_revision"] = 8
	expect(t, f.call(f.agent, "POST", path, body, lease), 409)
	body["policy_revision"] = 7

	// Members see redacted evidence through both the API and existing event
	// feed, including after the owning generation has stopped.
	reader := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	outsider := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		for _, p := range []tenant.Principal{reader, outsider} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','audit reader')`, p.TenantID, p.ID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, reader.TenantID, reader.ID, f.project)
		return err
	})
	expect(t, f.call(outsider, "GET", list, nil, ""), 403)
	expect(t, f.call(f.foreign, "GET", list, nil, ""), 403)
	w = f.call(reader, "GET", list, nil, "")
	expect(t, w, 200)
	items := decode(t, w)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["event_id"] != first["event_id"] {
		t.Fatal(w.Body.String())
	}
	for _, private := range []string{lease, "private-build-host", "private-account-name"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("private evidence leaked to member")
		}
	}
	events.New(f.db.App).Mount(f.mux)
	w = f.call(reader, "GET", "/api/events?node_id="+f.project+"&type=node.lead_decision_recorded", nil, "")
	expect(t, w, 200)
	if len(decode(t, w)["items"].([]any)) != 1 {
		t.Fatal("project event replay hid public evidence")
	}

	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp(),stop_reason='process_exited' WHERE id=$1`, session); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call(f.agent, "POST", path, body, lease), 403)
	expect(t, f.call(reader, "GET", list, nil, ""), 200)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET archived_at=clock_timestamp(),recovery_process_state='unknown',recovery_request_id=gen_random_uuid(),recovery_request_digest='fixture'::bytea,recovery_actor_id=agent_principal_id,recovery_reason='fixture' WHERE id=$1`, session); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call(f.agent, "POST", path, body, lease), 410)
	expect(t, f.call(reader, "GET", list, nil, ""), 200)
	if leadEventCount(t, f) != 1 {
		t.Fatal("rejected writers appended audit events")
	}
	if _, err := f.db.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, reader.TenantID, reader.ID); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call(reader, "GET", list, nil, ""), 403)
}

func TestLeadDecisionRechecksAccessAfterEarlierAuthorization(t *testing.T) {
	f := fixture(t)
	session, lease := leadFixture(t, f, "coordinator")
	path := "/api/projects/" + f.project + "/harness-sessions/" + session + "/lead-decisions"
	// The barrier holds a request after an earlier authorization succeeded,
	// before the handler takes the access fence and rechecks its final write.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	authorized := make(chan struct{})
	resume := make(chan struct{})
	ctx = tenant.WithPrincipal(ctx, f.agent)
	ctx = db.WithTenantGuard(ctx, func(ctx context.Context, tx pgx.Tx, _ string) error {
		if err := authz.RequireTx(ctx, tx, f.agent, "harness.worker", authz.Scope{ProjectID: f.project}); err != nil {
			return err
		}
		close(authorized)
		select {
		case <-resume:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	raw, err := json.Marshal(leadBody(f))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+f.key)
	r.Header.Set("X-Aeon-Worker-Lease", lease)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); f.mux.ServeHTTP(w, r) }()
	select {
	case <-authorized:
	case <-ctx.Done():
		t.Fatal("request never passed earlier authorization")
	}
	// Use the same authority fence as membership edits. There is no sleep and
	// the request cannot proceed before the revocation transaction commits.
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
		if err := authz.LockProjectMutation(ctx, tx, f.person.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.agent.TenantID, f.agent.ID)
		return err
	})
	close(resume)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("request never finished after revocation")
	}
	expect(t, w, 403)
	if !strings.Contains(w.Body.String(), "forbidden") || leadEventCount(t, f) != 0 {
		t.Fatal("earlier permission authorized final write")
	}
}

func TestLeadDecisionWaitPaginationAndRevocation(t *testing.T) {
	f := fixture(t)
	session, lease := leadFixture(t, f, "coordinator")
	path := "/api/projects/" + f.project + "/harness-sessions/" + session + "/lead-decisions"
	list := "/api/projects/" + f.project + "/lead-decisions"
	var now time.Time
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	gates := []any{}
	for _, kind := range []string{"dial", "harness", "account_room", "host_load"} {
		gates = append(gates, map[string]any{"kind": kind, "state": "ready", "observed_at": now})
	}
	gates[2] = map[string]any{"kind": "account_room", "state": "unreadable", "observed_at": nil}
	body := leadBody(f)
	body["stage"] = "admission"
	body["reason_codes"] = []string{"gates_ready"}
	body["gates"] = gates
	w := f.call(f.agent, "POST", path, body, lease)
	expect(t, w, 200)
	first := decode(t, w)["decision"].(map[string]any)
	if first["outcome"] != "wait" || !strings.Contains(w.Body.String(), "account_room_unreadable") {
		t.Fatal("unknown start gate was accepted")
	}
	// New reports and policy revisions never rewrite earlier replay snapshots.
	second := leadBody(f)
	second["policy_revision"] = 8
	expect(t, f.call(f.agent, "POST", path, second, lease), 200)
	w = f.call(f.agent, "POST", path, body, lease)
	expect(t, w, 200)
	if decode(t, w)["decision"].(map[string]any)["recorded_at"] != first["recorded_at"] {
		t.Fatal("replay recomputed freshness")
	}
	w = f.call(f.person, "GET", list+"?limit=1", nil, "")
	expect(t, w, 200)
	page := decode(t, w)
	if len(page["items"].([]any)) != 1 || page["next_after"] != first["event_id"] {
		t.Fatal(w.Body.String())
	}
	cursor := strconv.FormatInt(int64(page["next_after"].(float64)), 10)
	w = f.call(f.person, "GET", list+"?limit=1&after="+cursor, nil, "")
	expect(t, w, 200)
	page = decode(t, w)
	if len(page["items"].([]any)) != 1 || page["next_after"] != nil || page["items"].([]any)[0].(map[string]any)["request"].(map[string]any)["policy_revision"] != float64(8) {
		t.Fatal(w.Body.String())
	}
	w = f.call(f.person, "GET", list+"?session_id="+uid(), nil, "")
	expect(t, w, 200)
	if len(decode(t, w)["items"].([]any)) != 0 {
		t.Fatal("generation filter ignored")
	}
	for _, query := range []string{"?limit=201", "?limit=", "?after=-1", "?after=9223372036854775808", "?session_id=bad", "?limit=1&limit=2", "?prompt=hidden"} {
		expect(t, f.call(f.person, "GET", list+query, nil, ""), 400)
	}
	bad := leadBody(f)
	bad["prompt"] = "never store this"
	expect(t, f.call(f.agent, "POST", path, bad, lease), 400)
	partial := leadBody(f)
	partial["previous_event_id"] = first["event_id"]
	partial["attempt"] = 3
	expect(t, f.call(f.agent, "POST", path, partial, lease), 409)
	partial["attempt"] = 2
	partial["outcome"] = "partial"
	partial["reason_codes"] = []string{"partial_result", "model_escalated", "forecast_unavailable"}
	w = f.call(f.agent, "POST", path, partial, lease)
	expect(t, w, 200)
	if decode(t, w)["decision"].(map[string]any)["outcome"] != "partial" || !strings.Contains(w.Body.String(), "forecast_unavailable") || !strings.Contains(w.Body.String(), "previous_event_id") {
		t.Fatal("partial outcome or attempt lineage lost")
	}
	if _, err := f.db.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.agent.TenantID, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call(f.agent, "POST", path, body, lease), 403)
	if leadEventCount(t, f) != 3 {
		t.Fatal("revoked generation replayed or appended")
	}
	if authz.RoutePermissions["GET /api/projects/{projectId}/lead-decisions"] != "nodes.read" || authz.RoutePermissions["POST /api/projects/{projectId}/harness-sessions/{sessionId}/lead-decisions"] != "harness.worker" {
		t.Fatal("production route authority missing")
	}
}

func TestLeadDecisionResultBindingsAndRollback(t *testing.T) {
	f := fixture(t)
	session, lease := leadFixture(t, f, "coordinator")
	path := "/api/projects/" + f.project + "/harness-sessions/" + session + "/lead-decisions"
	body := leadBody(f)
	body["stage"] = "review"
	body["outcome"] = "requested"
	body["reason_codes"] = []string{"review_requested"}
	// Persist a real review binding without exposing its request/prompt/result.
	order := uid()
	profile := uid()
	request := uid()
	base := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,$3,'test-version','claude','anthropic','claude-opus-4-1','xhigh','frontier')`, f.person.TenantID, profile, "lead-review-"+profile); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id) SELECT tenant_id,$1,'HTS-3',id,'Review',$2 FROM node_kinds WHERE slug='work_order'`, order, f.ticket); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,kind,status) VALUES($1,$2,$3,'review','blocked')`, f.person.TenantID, order, f.person.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO work_order_reviews(tenant_id,work_order_id,ticket_node_id,request_id,request,repository,base_sha,head_sha,author_family,reviewer_profile_id,reviewer_family,ticket_snapshot) VALUES($1,$2,$3,$4,'{"private":"prompt"}','inspr-at/paimos',$5,$6,'openai',$7,'anthropic','private review prompt')`, f.person.TenantID, order, f.ticket, request, base, head, profile)
		return err
	})
	body["results"] = []any{map[string]any{"kind": "review", "id": order, "head_sha": strings.Repeat("c", 40)}}
	expect(t, f.call(f.agent, "POST", path, body, lease), 409)
	if leadEventCount(t, f) != 0 {
		t.Fatal("failed result binding appended evidence")
	}
	body["results"] = []any{map[string]any{"kind": "review", "id": order, "head_sha": head}}
	w := f.call(f.agent, "POST", path, body, lease)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"base_sha":"`+base+`"`) || !strings.Contains(w.Body.String(), `"author_family":"openai"`) || !strings.Contains(w.Body.String(), `"reviewer_family":"anthropic"`) || strings.Contains(w.Body.String(), "private review") || strings.Contains(w.Body.String(), `"gate_open"`) {
		t.Fatal("review binding was lost or granted authority")
	}
	bad := leadBody(f)
	bad["results"] = []any{map[string]any{"kind": "run", "id": uid()}}
	expect(t, f.call(f.agent, "POST", path, bad, lease), 400)
	if leadEventCount(t, f) != 1 {
		t.Fatal("missing result appended evidence")
	}
	run, release := uid(), uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,id,work_order_id,agent_principal_id) VALUES($1,$2,$3,$4)`, f.agent.TenantID, run, order, f.agent.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id) SELECT $1,$2,'HTS-4',id,'Release',$3 FROM node_kinds WHERE slug='release'`, f.person.TenantID, release, f.project)
		return err
	})
	handoff := leadBody(f)
	handoff["stage"] = "release_handoff"
	handoff["outcome"] = "handoff"
	handoff["reason_codes"] = []string{"release_handoff"}
	handoff["results"] = []any{map[string]any{"kind": "run", "id": run}, map[string]any{"kind": "release", "id": release}}
	w = f.call(f.agent, "POST", path, handoff, lease)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), "person_gate_required") || len(decode(t, w)["decision"].(map[string]any)["results"].([]any)) != 2 {
		t.Fatal("handoff lost result references or person gate")
	}
	partial := leadBody(f)
	partial["results"] = []any{map[string]any{"kind": "run", "id": run}, map[string]any{"kind": "run", "id": uid()}}
	expect(t, f.call(f.agent, "POST", path, partial, lease), 400)
	if leadEventCount(t, f) != 2 {
		t.Fatal("partial invalid result list reported success")
	}
	// A different live generation cannot hijack a recorded request ID.
	other, otherLease := leadFixture(t, f, "coordinator")
	expect(t, f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+other+"/lead-decisions", body, otherLease), 409)
}

func TestLeadDecisionRequestIdentityNeverAddsHiddenNodeReference(t *testing.T) {
	f := fixture(t)
	session, lease := leadFixture(t, f, "coordinator")
	reader := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	hiddenProject := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','project reader')`, reader.TenantID, reader.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, reader.TenantID, reader.ID, f.project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'HTS-5',id,'Hidden project' FROM node_kinds WHERE slug='project'`, f.person.TenantID, hiddenProject)
		return err
	})
	body := leadBody(f)
	body["request_id"] = hiddenProject
	path := "/api/projects/" + f.project + "/harness-sessions/" + session + "/lead-decisions"
	expect(t, f.call(f.agent, "POST", path, body, lease), 200)
	w := f.call(reader, "GET", "/api/projects/"+f.project+"/lead-decisions", nil, "")
	expect(t, w, 200)
	if len(decode(t, w)["items"].([]any)) != 1 {
		t.Fatal("request identity was mistaken for a hidden node reference")
	}
	w = f.call(f.agent, "POST", path, body, lease)
	expect(t, w, 200)
	if decode(t, w)["replayed"] != true || leadEventCount(t, f) != 1 {
		t.Fatal("request identity broke idempotency")
	}
}
