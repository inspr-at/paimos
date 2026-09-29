// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
)

func (f *harnessFixture) addNode(t *testing.T, id, key, kind, parent, title string) {
	t.Helper()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var kindID string
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM node_kinds WHERE slug=$1`, kind).Scan(&kindID); err != nil {
			return err
		}
		var parentID any
		if parent != "" {
			parentID = parent
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id) VALUES($1,$2,$3,$4,$5,$6)`, f.person.TenantID, id, key, kindID, title, parentID)
		return err
	})
}

func (f *harnessFixture) addAgent(t *testing.T, id, name string) {
	t.Helper()
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'agent',$3)`, f.agent.TenantID, id, name)
		return err
	})
}

func (f *harnessFixture) registerSession(t *testing.T, agentID, role, ticket, ref, lease string) string {
	t.Helper()
	body := map[string]any{
		"agent_principal_id": agentID, "harness": "codex", "host": "build-host",
		"harness_session_ref": ref, "worker_lease": lease, "management_mode": "managed",
		"role": role, "advertised_capabilities": []string{"inbox", "status"},
	}
	if ticket != "" {
		body["ticket_node_id"] = ticket
		body["work_shape"] = "ship"
	}
	w := f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", body, "")
	expect(t, w, 201)
	return decode(t, w)["id"].(string)
}

func (f *harnessFixture) beat(t *testing.T, session, lease string, seq int, extra map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"phase": "working", "activity": "busy", "activity_sequence": seq}
	for k, v := range extra {
		body[k] = v
	}
	w := f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+session+"/heartbeat", body, lease)
	expect(t, w, 200)
	return decode(t, w)
}

type etaRow struct {
	ready, live           *time.Time
	progress              *int
	readyBy, liveBy       *string
	readyStale, liveStale bool
}

func (f *harnessFixture) nodeEta(t *testing.T, id string) etaRow {
	t.Helper()
	var row etaRow
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT eta_ready_at, eta_live_at, progress_pct, ready_by, live_by, ready_stale, live_stale FROM aeon_node_eta($1::uuid)`, id).
			Scan(&row.ready, &row.live, &row.progress, &row.readyBy, &row.liveBy, &row.readyStale, &row.liveStale)
	})
	return row
}

func TestEtaHeartbeatValidationStaleAndRollup(t *testing.T) {
	f := fixture(t)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE principals SET name='Ada' WHERE id=$1`, f.agent.ID)
		return err
	})
	lease := "eta-lease-000000000000000000000001"
	session := f.registerSession(t, f.agent.ID, "worker", f.ticket, "eta-ref-0000000000000001", lease)
	path := "/api/projects/" + f.project + "/harness-sessions/" + session

	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1, "progress_pct": 101}, lease), 400)
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1, "progress_pct": -1}, lease), 400)
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1, "eta_live_at": time.Now().Add(time.Hour).Format(time.RFC3339)}, lease), 400)
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1, "eta_ready_at": time.Now().Add(-31 * 24 * time.Hour).Format(time.RFC3339)}, lease), 400)
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1, "eta_ready_at": time.Now().Add(366 * 24 * time.Hour).Format(time.RFC3339)}, lease), 400)

	past := time.Now().Add(-5 * time.Minute).UTC().Truncate(time.Second)
	got := f.beat(t, session, lease, 1, map[string]any{"eta_ready_at": past.Format(time.RFC3339), "progress_pct": 0})
	if got["progress_pct"] != float64(0) {
		t.Fatalf("progress 0: %#v", got["progress_pct"])
	}
	ready, err := time.Parse(time.RFC3339, got["eta_ready_at"].(string))
	if err != nil || ready.After(time.Now()) {
		t.Fatalf("past ETA not stored as overdue input: %v %v", got["eta_ready_at"], err)
	}
	reported := got["eta_reported_at"].(string)
	again := f.beat(t, session, lease, 2, nil)
	if again["eta_reported_at"] != reported || again["progress_pct"] != float64(0) {
		t.Fatalf("omitted estimate changed the report: %#v", again)
	}
	if _, ok := again["eta_stale"]; ok {
		t.Fatal("a fresh estimate was marked stale")
	}
	cleared := f.beat(t, session, lease, 3, map[string]any{"eta_ready_at": nil, "progress_pct": nil})
	if _, ok := cleared["eta_ready_at"]; ok {
		t.Fatalf("cleared estimate still present: %#v", cleared)
	}
	if _, ok := cleared["progress_pct"]; ok {
		t.Fatalf("cleared percent still present: %#v", cleared)
	}
	if _, ok := cleared["eta_reported_at"]; ok {
		t.Fatal("clearing the estimate left a report time")
	}

	full := f.beat(t, session, lease, 4, map[string]any{"progress_pct": 100, "eta_ready_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
	if full["progress_pct"] != float64(100) {
		t.Fatalf("progress 100: %#v", full["progress_pct"])
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET eta_reported_at = clock_timestamp() - interval '19 minutes' WHERE id=$1`, session)
		return err
	})
	status := decode(t, f.call(f.person, "GET", path, nil, ""))
	if status["needs_attention"] == true || status["eta_stale"] == true {
		t.Fatalf("19 minutes is inside two intervals: %#v", status)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET eta_reported_at = clock_timestamp() - interval '21 minutes' WHERE id=$1`, session)
		return err
	})
	status = decode(t, f.call(f.person, "GET", path, nil, ""))
	if status["needs_attention"] == true || status["eta_stale"] != true {
		t.Fatalf("21 minutes should be eta_stale without needs_attention: %#v", status)
	}
	for _, item := range status["attention_reasons"].([]any) {
		if item.(map[string]any)["kind"] == "eta_stale" {
			t.Fatalf("eta_stale is not an attention reason: %#v", status["attention_reasons"])
		}
	}

	expect(t, f.call(f.person, "PUT", "/api/settings/eta-interval", map[string]any{"interval_minutes": 0}, ""), 400)
	expect(t, f.call(f.person, "PUT", "/api/settings/eta-interval", map[string]any{"interval_minutes": 241}, ""), 400)
	if decode(t, f.call(f.person, "GET", "/api/settings/eta-interval", nil, ""))["interval_minutes"] != float64(10) {
		t.Fatal("default interval is not 10")
	}
	expect(t, f.call(f.person, "PUT", "/api/settings/eta-interval", map[string]any{"interval_minutes": 1}, ""), 200)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET eta_reported_at = clock_timestamp() - interval '3 minutes' WHERE id=$1`, session)
		return err
	})
	if decode(t, f.call(f.person, "GET", path, nil, ""))["eta_stale"] != true {
		t.Fatal("three minutes is stale once the interval is one minute")
	}
	expect(t, f.call(f.person, "PUT", "/api/settings/eta-interval", map[string]any{"interval_minutes": 10}, ""), 200)

	coordLease := "eta-lease-000000000000000000000002"
	coord := f.registerSession(t, f.agent.ID, "coordinator", f.ticket, "eta-ref-0000000000000002", coordLease)
	expect(t, f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+coord+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1, "eta_ready_at": time.Now().Add(time.Hour).Format(time.RFC3339)}, coordLease), 400)
	liveAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	live := f.beat(t, coord, coordLease, 1, map[string]any{"eta_live_at": liveAt.Format(time.RFC3339)})
	if _, ok := live["eta_live_at"]; !ok {
		t.Fatalf("coordinator live ETA missing: %#v", live)
	}

	task, epic, bare := uid(), uid(), uid()
	f.addNode(t, task, "ETA-2", "task", f.project, "Task with a live estimate")
	f.addNode(t, epic, "ETA-3", "epic", f.project, "Epic rejects a direct live estimate")
	f.addNode(t, bare, "ETA-4", "ticket", f.project, "No agent")
	expect(t, f.call(f.agent, "PUT", "/api/nodes/"+task+"/live-eta", map[string]any{"eta_live_at": liveAt.Format(time.RFC3339)}, "wrong-lease-000000000000000000000099"), 403)
	taskWorker := f.registerSession(t, f.agent.ID, "worker", task, "eta-ref-0000000000000003", "eta-lease-000000000000000000000003")
	w := f.call(f.agent, "PUT", "/api/nodes/"+task+"/live-eta", map[string]any{"eta_live_at": liveAt.Format(time.RFC3339)}, coordLease)
	expect(t, w, 200)
	if decode(t, w)["eta_live_at"] == nil {
		t.Fatalf("task live ETA: %s", w.Body.String())
	}
	expect(t, f.call(f.agent, "PUT", "/api/nodes/"+epic+"/live-eta", map[string]any{"eta_live_at": liveAt.Format(time.RFC3339)}, coordLease), 400)
	if row := f.nodeEta(t, bare); row.ready != nil || row.live != nil || row.progress != nil || row.readyStale || row.liveStale {
		t.Fatalf("unknown ticket guessed an estimate: %+v", row)
	}
	if row := f.nodeEta(t, task); row.live == nil || row.liveBy == nil || *row.liveBy != "Ada" {
		t.Fatalf("task live ETA: %+v", row)
	}

	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped', stopped_at=clock_timestamp(), stop_reason='completed', eta_ready_at=clock_timestamp() + interval '1 hour', progress_pct=40, eta_reported_at=clock_timestamp() WHERE id=$1`, taskWorker)
		return err
	})
	if row := f.nodeEta(t, task); row.ready != nil || row.live != nil || row.progress != nil {
		t.Fatalf("stopped session still shows an estimate: %+v", row)
	}

	beau := uid()
	f.addAgent(t, beau, "Beau")
	flat, inner, outer := uid(), uid(), uid()
	t1, t2, t3 := uid(), uid(), uid()
	n1, n2, n3 := uid(), uid(), uid()
	f.addNode(t, flat, "ETA-5", "epic", f.project, "Flat epic")
	f.addNode(t, t1, "ETA-6", "ticket", flat, "Half")
	f.addNode(t, t2, "ETA-7", "ticket", flat, "Done")
	f.addNode(t, t3, "ETA-8", "ticket", flat, "Unknown child")
	f.addNode(t, outer, "ETA-9", "epic", f.project, "Outer epic")
	f.addNode(t, n1, "ETA-10", "ticket", outer, "Finished child")
	f.addNode(t, inner, "ETA-11", "epic", outer, "Inner epic")
	f.addNode(t, n2, "ETA-12", "ticket", inner, "Zero a")
	f.addNode(t, n3, "ETA-13", "ticket", inner, "Zero b")
	s1 := f.registerSession(t, f.agent.ID, "worker", t1, "eta-ref-0000000000000011", "eta-lease-000000000000000000000011")
	s2 := f.registerSession(t, beau, "worker", t2, "eta-ref-0000000000000012", "eta-lease-000000000000000000000012")
	_ = f.registerSession(t, f.agent.ID, "worker", t3, "eta-ref-0000000000000013", "eta-lease-000000000000000000000013")
	sn1 := f.registerSession(t, f.agent.ID, "worker", n1, "eta-ref-0000000000000021", "eta-lease-000000000000000000000021")
	sn2 := f.registerSession(t, f.agent.ID, "worker", n2, "eta-ref-0000000000000022", "eta-lease-000000000000000000000022")
	sn3 := f.registerSession(t, beau, "worker", n3, "eta-ref-0000000000000023", "eta-lease-000000000000000000000023")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET eta_ready_at=clock_timestamp() + interval '10 minutes', progress_pct=50, eta_reported_at=clock_timestamp() WHERE id=$1`, s1); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET eta_ready_at=clock_timestamp() + interval '40 minutes', progress_pct=100, eta_reported_at=clock_timestamp() WHERE id=$1`, s2); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET eta_ready_at=clock_timestamp() + interval '10 minutes', progress_pct=100, eta_reported_at=clock_timestamp() WHERE id=$1`, sn1); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET eta_ready_at=clock_timestamp() + interval '5 minutes', progress_pct=0, eta_reported_at=clock_timestamp() WHERE id=$1`, sn2); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET eta_ready_at=clock_timestamp() + interval '80 minutes', progress_pct=0, eta_reported_at=clock_timestamp() WHERE id=$1`, sn3)
		return err
	})
	flatRow := f.nodeEta(t, flat)
	if flatRow.progress == nil || *flatRow.progress != 75 {
		t.Fatalf("flat percent %v, want 75", flatRow.progress)
	}
	if flatRow.ready == nil || flatRow.readyBy == nil || *flatRow.readyBy != "Beau" {
		t.Fatalf("flat ready should be Beau's later estimate: %+v", flatRow)
	}
	if flatRow.ready.Before(time.Now().Add(30*time.Minute)) || flatRow.readyStale {
		t.Fatalf("flat ready time %+v", flatRow)
	}
	outerRow := f.nodeEta(t, outer)
	if outerRow.progress == nil || *outerRow.progress != 50 {
		t.Fatalf("nested percent %v, want 50", outerRow.progress)
	}
	if outerRow.ready == nil || outerRow.ready.Before(time.Now().Add(70*time.Minute)) || outerRow.readyBy == nil || *outerRow.readyBy != "Beau" {
		t.Fatalf("nested ready should be the furthest child: %+v", outerRow)
	}
	unknown := f.nodeEta(t, t3)
	if unknown.ready != nil || unknown.progress != nil || unknown.readyStale {
		t.Fatalf("active session without a report must stay empty: %+v", unknown)
	}
	var exact, pastBoundary bool
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT
			(now() - aeon_eta_interval() * 2) < now() - aeon_eta_interval() * 2,
			(now() - aeon_eta_interval() * 2 - interval '1 second') < now() - aeon_eta_interval() * 2`).Scan(&exact, &pastBoundary)
	})
	if exact || !pastBoundary {
		t.Fatalf("staleness boundary exact=%v past=%v", exact, pastBoundary)
	}

	var order []string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT f.id::text FROM unnest($1::uuid[]) WITH ORDINALITY AS f(id, ord)
			LEFT JOIN LATERAL aeon_node_eta(f.id) eta ON true
			ORDER BY eta.eta_ready_at IS NULL ASC, eta.eta_ready_at ASC, f.ord`, []string{t2, t3, t1})
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			order = append(order, id)
		}
		return rows.Err()
	})
	if len(order) != 3 || order[0] != t1 || order[1] != t2 || order[2] != t3 {
		t.Fatalf("sort order %v, want earlier, later, unknown", order)
	}
}

func TestHarnessReporterOmitsUnusedEta(t *testing.T) {
	f := fixture(t)
	lease := "eta-lease-000000000000000000000101"
	session := f.registerSession(t, f.agent.ID, "worker", f.ticket, "eta-ref-0000000000000101", lease)
	path := "/api/projects/" + f.project + "/harness-sessions/" + session
	beat := f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}, lease)
	expect(t, beat, 200)
	status := f.call(f.person, "GET", path, nil, "")
	expect(t, status, 200)
	legacy := map[string]bool{"approval": true, "held_action": true, "reply": true, "reply_due": true, "run_waiting": true, "session_yielded": true}
	for _, tc := range []struct {
		name string
		body map[string]any
		got  string
	}{
		{"heartbeat", decode(t, beat), beat.Header().Get("Aeon-Contract")},
		{"status", decode(t, status), status.Header().Get("Aeon-Contract")},
	} {
		if tc.got != "harness-session/1.2" {
			t.Fatalf("%s Aeon-Contract = %q", tc.name, tc.got)
		}
		for _, key := range []string{"eta_ready_at", "eta_live_at", "progress_pct", "eta_reported_at", "eta_stale"} {
			if _, ok := tc.body[key]; ok {
				t.Fatalf("%s included %s without an estimate: %#v", tc.name, key, tc.body)
			}
		}
		reasons, _ := tc.body["attention_reasons"].([]any)
		for _, item := range reasons {
			kind, _ := item.(map[string]any)["kind"].(string)
			if !legacy[kind] {
				t.Fatalf("%s attention kind %q is outside the legacy enum", tc.name, kind)
			}
		}
	}
	if _, ok := decode(t, beat)["needs_attention"]; ok {
		t.Fatal("heartbeat projected needs_attention")
	}
	if statusBody := decode(t, status); statusBody["needs_attention"] != false {
		t.Fatalf("status needs_attention %#v", statusBody["needs_attention"])
	}
}

func TestRebindClearsEstimate(t *testing.T) {
	f := fixture(t)
	lease := "eta-lease-000000000000000000000102"
	session := f.registerSession(t, f.agent.ID, "worker", f.ticket, "eta-ref-0000000000000102", lease)
	ready := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	got := f.beat(t, session, lease, 1, map[string]any{"eta_ready_at": ready, "progress_pct": 80})
	revision := int(got["revision"].(float64))
	other := uid()
	f.addNode(t, other, "ETA-20", "ticket", f.project, "Rebound ticket")
	w := f.call(f.person, "PATCH", "/api/projects/"+f.project+"/harness-sessions/"+session+"/binding", map[string]any{
		"expected_revision": revision, "ticket_node_id": other, "work_shape": "ship",
	}, "")
	expect(t, w, 200)
	bound := decode(t, w)
	for _, key := range []string{"eta_ready_at", "eta_live_at", "progress_pct", "eta_reported_at"} {
		if _, ok := bound[key]; ok {
			t.Fatalf("rebind kept %s: %#v", key, bound)
		}
	}
	if row := f.nodeEta(t, other); row.ready != nil || row.progress != nil || row.readyStale {
		t.Fatalf("rebound ticket inherited the previous estimate: %+v", row)
	}
}

func TestLiveEtaFollowsCurrentProject(t *testing.T) {
	f := fixture(t)
	lease := "eta-lease-000000000000000000000103"
	session := f.registerSession(t, f.agent.ID, "coordinator", f.ticket, "eta-ref-0000000000000103", lease)
	first := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	f.beat(t, session, lease, 1, map[string]any{"eta_live_at": first.Format(time.RFC3339)})
	var reported time.Time
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT reported_at FROM ticket_live_eta WHERE node_id=$1`, f.ticket).Scan(&reported)
	})
	other := uid()
	f.addNode(t, other, "ETA-21", "project", "", "Moved project")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.ticket, other)
		return err
	})
	var project string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT project_id::text FROM nodes WHERE id=$1`, f.ticket).Scan(&project)
	})
	if project != other {
		t.Fatalf("ticket project = %s, want %s", project, other)
	}
	moved := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	expect(t, f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+session+"/heartbeat", map[string]any{
		"phase": "working", "activity": "busy", "activity_sequence": 2, "eta_live_at": moved,
	}, lease), 403)
	var again time.Time
	var live time.Time
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT eta_live_at, reported_at FROM ticket_live_eta WHERE node_id=$1`, f.ticket).Scan(&live, &again)
	})
	if !live.Equal(first) || !again.Equal(reported) {
		t.Fatalf("moved ticket live ETA changed to %s reported %s", live, again)
	}
}

func TestLiveEtaWaitsOutAProjectMove(t *testing.T) {
	f := fixture(t)
	lease := "eta-lease-000000000000000000000108"
	session := f.registerSession(t, f.agent.ID, "coordinator", f.ticket, "eta-ref-0000000000000108", lease)
	first := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	f.beat(t, session, lease, 1, map[string]any{"eta_live_at": first.Format(time.RFC3339)})
	var live, reported time.Time
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT eta_live_at, reported_at FROM ticket_live_eta WHERE node_id=$1`, f.ticket).Scan(&live, &reported)
	})
	target := uid()
	f.addNode(t, target, "DST-1", "project", "", "Destination project")

	// Hold the key counter the move takes only after it has locked the ticket
	// FOR UPDATE, so the ETA's FOR SHARE can wait, the move can then commit,
	// and the resumed read has to see the new project.
	blocker, err := f.db.Admin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err = blocker.Exec(context.Background(), `INSERT INTO node_key_counters(tenant_id,prefix,last_number) VALUES($1,'DST',1)
		ON CONFLICT (tenant_id, prefix) DO UPDATE SET last_number=node_key_counters.last_number`, f.person.TenantID); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	nodes.New(f.db.App, nil).Mount(mux)
	moveCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest(http.MethodPost, "/api/nodes/"+f.ticket+"/project-move", strings.NewReader(`{"project_id":"`+target+`"}`))
		r = r.WithContext(tenant.WithPrincipal(context.Background(), f.person))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		moveCh <- w
	}()
	waitForLock(t, f, "%aeon_next_node_key%")

	etaCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		etaCh <- f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+session+"/heartbeat", map[string]any{
			"phase": "working", "activity": "busy", "activity_sequence": 2,
			"eta_live_at": time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339),
		}, lease)
	}()
	waitForLock(t, f, "%FOR SHARE OF n%")
	if err = blocker.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}

	moved := <-moveCh
	expect(t, moved, 200)
	eta := <-etaCh
	if eta.Code != 403 && eta.Code != 409 {
		t.Fatalf("stale coordinator live ETA: %d %s", eta.Code, eta.Body.String())
	}
	if !strings.Contains(eta.Body.String(), "live ETA stays with the ticket's current project") {
		t.Fatalf("unexpected refusal: %s", eta.Body.String())
	}
	var project string
	var againLive, againReported time.Time
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT project_id::text FROM nodes WHERE id=$1`, f.ticket).Scan(&project); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT eta_live_at, reported_at FROM ticket_live_eta WHERE node_id=$1`, f.ticket).Scan(&againLive, &againReported)
	})
	if project != target {
		t.Fatalf("ticket project = %s, want %s", project, target)
	}
	if !againLive.Equal(live) || !againReported.Equal(reported) {
		t.Fatalf("moved ticket live ETA changed to %s reported %s", againLive, againReported)
	}
}

func waitForLock(t *testing.T, f *harnessFixture, queryLike string) {
	t.Helper()
	var waiting int
	var err error
	for range 300 {
		err = f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1`, queryLike).Scan(&waiting)
		if err != nil || waiting > 0 {
			break
		}
		if _, err = f.db.Admin.Exec(t.Context(), `SELECT pg_sleep(0.01)`); err != nil {
			break
		}
	}
	if err != nil || waiting == 0 {
		t.Fatalf("timed out waiting for lock %s: %d %v", queryLike, waiting, err)
	}
}

func TestLiveEtaUsesTheLeaseProof(t *testing.T) {
	f := fixture(t)
	boundLease := "eta-lease-000000000000000000000104"
	otherLease := "eta-lease-000000000000000000000105"
	bound := f.registerSession(t, f.agent.ID, "coordinator", f.ticket, "eta-ref-0000000000000104", boundLease)
	f.beat(t, bound, boundLease, 1, nil)
	other := f.registerSession(t, f.agent.ID, "coordinator", "", "eta-ref-0000000000000105", otherLease)
	liveAt := time.Now().Add(4 * time.Hour).UTC().Truncate(time.Second)
	w := f.call(f.agent, "PUT", "/api/nodes/"+f.ticket+"/live-eta", map[string]any{"eta_live_at": liveAt.Format(time.RFC3339)}, otherLease)
	expect(t, w, 200)
	if decode(t, w)["eta_live_at"] == nil {
		t.Fatalf("lease-matched coordinator was rejected: %s", w.Body.String())
	}
	var stamped int
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_sessions WHERE id=$1 AND eta_reported_at IS NOT NULL`, bound).Scan(&stamped)
	})
	if stamped != 0 {
		t.Fatal("the bound session absorbed a live ETA proved by the other lease")
	}
	if other == "" {
		t.Fatal("missing coordinator session")
	}
}

func TestLiveEtaTimestampsArePerTicket(t *testing.T) {
	f := fixture(t)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE principals SET name='Ada' WHERE id=$1`, f.agent.ID)
		return err
	})
	lease := "eta-lease-000000000000000000000106"
	coord := f.registerSession(t, f.agent.ID, "coordinator", f.ticket, "eta-ref-0000000000000106", lease)
	liveAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	first := f.beat(t, coord, lease, 1, map[string]any{"eta_live_at": liveAt.Format(time.RFC3339)})
	reported := first["eta_reported_at"].(string)
	other := uid()
	f.addNode(t, other, "ETA-22", "ticket", f.project, "Other ticket")
	f.registerSession(t, f.agent.ID, "worker", other, "eta-ref-0000000000000107", "eta-lease-000000000000000000000107")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE ticket_live_eta SET reported_at=clock_timestamp() - interval '21 minutes' WHERE node_id=$1`, f.ticket); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET eta_reported_at=clock_timestamp() - interval '21 minutes' WHERE id=$1`, coord)
		return err
	})
	later := time.Now().Add(5 * time.Hour).UTC().Format(time.RFC3339)
	expect(t, f.call(f.agent, "PUT", "/api/nodes/"+other+"/live-eta", map[string]any{"eta_live_at": later}, lease), 200)
	if row := f.nodeEta(t, f.ticket); !row.liveStale {
		t.Fatalf("refreshing another ticket cleared the first ticket's staleness: %+v", row)
	}
	if row := f.nodeEta(t, other); row.live == nil || row.liveStale {
		t.Fatalf("the other ticket should be fresh: %+v", row)
	}
	status := decode(t, f.call(f.person, "GET", "/api/projects/"+f.project+"/harness-sessions/"+coord, nil, ""))
	if status["eta_stale"] != true || status["eta_reported_at"] == reported {
		t.Fatalf("session timestamp was refreshed by another ticket: original %s now %#v", reported, status["eta_reported_at"])
	}
	expect(t, f.call(f.agent, "PUT", "/api/nodes/"+other+"/live-eta", map[string]any{"eta_live_at": nil}, lease), 200)
	if row := f.nodeEta(t, other); row.live != nil || row.liveStale {
		t.Fatalf("clearing the other ticket left its estimate: %+v", row)
	}
	cleared := decode(t, f.call(f.person, "GET", "/api/projects/"+f.project+"/harness-sessions/"+coord, nil, ""))
	if cleared["eta_stale"] != true || cleared["eta_reported_at"] == nil {
		t.Fatalf("clearing an unbound ticket changed the bound session timestamp: %#v", cleared)
	}
	expect(t, f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+coord+"/heartbeat", map[string]any{
		"phase": "working", "activity": "busy", "activity_sequence": 2, "eta_live_at": nil,
	}, lease), 200)
	gone := decode(t, f.call(f.person, "GET", "/api/projects/"+f.project+"/harness-sessions/"+coord, nil, ""))
	if _, ok := gone["eta_reported_at"]; ok || gone["eta_stale"] == true {
		t.Fatalf("clearing the bound estimate left its timestamp: %#v", gone)
	}
	if row := f.nodeEta(t, f.ticket); row.live != nil {
		t.Fatalf("bound clear left the ticket live ETA: %+v", row)
	}
}

func TestEtaMigrationLockAndValidation(t *testing.T) {
	add, err := os.ReadFile("../db/migrations/0907_eta_progress.sql")
	if err != nil {
		t.Fatal(err)
	}
	validate, err := os.ReadFile("../db/migrations/0908_eta_progress_validate.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{string(add), string(validate)} {
		if !strings.Contains(body, "SET LOCAL lock_timeout = '5s'") {
			t.Fatal("migration is missing lock_timeout")
		}
	}
	if !strings.Contains(string(add), "NOT VALID") || strings.Contains(string(add), "VALIDATE CONSTRAINT") {
		t.Fatal("0907 must add the check NOT VALID and leave validation to 0908")
	}
	if !strings.Contains(string(add), "harness_sessions is small") {
		t.Fatal("0907 must say why a plain index on harness_sessions is acceptable")
	}
	if !strings.Contains(string(validate), "VALIDATE CONSTRAINT harness_sessions_progress_pct") {
		t.Fatal("0908 must validate the progress check")
	}
	f := fixture(t)
	var validated bool
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT convalidated FROM pg_constraint WHERE conname='harness_sessions_progress_pct'`).Scan(&validated)
	})
	if !validated {
		t.Fatal("progress check is not validated")
	}
}

func TestSessionEtaStalenessUsesDatabaseClock(t *testing.T) {
	// Same shape as AEON-271: production reads clock_timestamp(), and injected
	// clocks on either side of the wall clock prove time.Now() is not consulted.
	for _, year := range []int{0, 2001, 2099} {
		name := "database"
		now := time.Date(year, time.January, 1, 11, 0, 0, 0, time.UTC)
		var clock func() time.Time
		if year != 0 {
			name = now.Format("2006")
			clock = func() time.Time { return now }
		}
		t.Run(name, func(t *testing.T) {
			f := fixtureWithOwnershipClock(t, clock)
			if clock == nil {
				f.tx(t, f.person, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now)
				})
			}
			lease := "eta-clock-lease-000000000000000001"
			session := f.registerSession(t, f.agent.ID, "worker", f.ticket, "eta-clock-ref-0000000000000001", lease)
			path := "/api/projects/" + f.project + "/harness-sessions/" + session
			setAge := func(age time.Duration, stopped bool) {
				t.Helper()
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE harness_sessions
						SET eta_ready_at=$2, progress_pct=40, eta_reported_at=$3,
						    phase=CASE WHEN $4 THEN 'stopped' ELSE 'working' END,
						    stopped_at=CASE WHEN $4 THEN $5 ELSE NULL::timestamptz END,
						    stop_reason=CASE WHEN $4 THEN 'completed' ELSE NULL END
						WHERE id=$1`, session, now.Add(time.Hour), now.Add(-age), stopped, now)
					return err
				})
			}
			stale := func() bool {
				t.Helper()
				status := decode(t, f.call(f.person, "GET", path, nil, ""))
				value, ok := status["eta_stale"]
				if !ok {
					return false
				}
				got, isBool := value.(bool)
				if !isBool || !got {
					t.Fatalf("eta_stale %#v", value)
				}
				return true
			}
			setAge(19*time.Minute, false)
			if stale() {
				t.Fatal("19 minutes is inside two intervals")
			}
			setAge(21*time.Minute, false)
			if !stale() {
				t.Fatal("21 minutes is past two intervals")
			}
			if clock == nil {
				return
			}
			setAge(20*time.Minute, false)
			if stale() {
				t.Fatal("exactly two intervals is still fresh")
			}
			setAge(20*time.Minute+time.Millisecond, false)
			if !stale() {
				t.Fatal("one millisecond past two intervals is stale")
			}
			setAge(21*time.Minute, true)
			if stale() {
				t.Fatal("a stopped session is not stale")
			}
		})
	}
}
