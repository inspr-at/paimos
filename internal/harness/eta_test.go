// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
	if status["needs_attention"] != true || status["eta_stale"] != true {
		t.Fatalf("21 minutes should be stale: %#v", status)
	}
	reasons, _ := status["attention_reasons"].([]any)
	found := false
	for _, item := range reasons {
		if item.(map[string]any)["kind"] == "eta_stale" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing eta_stale reason: %#v", status["attention_reasons"])
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
