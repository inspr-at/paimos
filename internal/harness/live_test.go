// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/tenant"
)

// AEON-184: the Projects page asks once which agents are working where. Only
// fresh, unstopped, non-idle sessions count; project visibility decides what
// exists; names and session ids follow the caller's permissions.
func TestLiveAgents(t *testing.T) {
	f := fixture(t)
	ctx := t.Context()
	second := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'HTS-3',kind_id,'Second project' FROM nodes WHERE id=$3`, f.person.TenantID, second, f.project)
		return err
	})
	register := func(project string, ticket bool) string {
		t.Helper()
		body := map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "studio-mac", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": "live-generation-" + uid(), "worker_lease": "live-worker-lease-" + uid()}
		if ticket {
			body["ticket_node_id"] = f.ticket
			body["work_shape"] = "ship"
		}
		w := f.call(f.person, "POST", "/api/projects/"+project+"/harness-sessions", body, "")
		expect(t, w, 201)
		return decode(t, w)["id"].(string)
	}
	// state sets what a heartbeat would; beat is how long ago it arrived (nil: never).
	state := func(id, phase, activity string, beat *string, stopped bool) {
		t.Helper()
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE harness_sessions SET phase=$2,activity=$3,heartbeat_at=CASE WHEN $4::text IS NULL THEN NULL ELSE clock_timestamp()-($4::text)::interval END,stopped_at=CASE WHEN $5 THEN clock_timestamp() END,stop_reason=CASE WHEN $5 THEN 'stopped' END WHERE id=$1`, id, phase, activity, beat, stopped)
			return err
		})
	}
	ago := func(s string) *string { return &s }
	working := register(f.project, true)
	state(working, "working", "busy", ago("20 seconds"), false)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE harness_sessions SET activity_note='Reviewing project changes' WHERE id=$1`, working)
		return err
	})
	starting := register(second, false)
	state(starting, "starting", "unknown", ago("5 seconds"), false)
	stale := register(f.project, false)
	state(stale, "working", "busy", ago("3 minutes"), false)
	idle := register(f.project, false)
	state(idle, "working", "idle", ago("10 seconds"), false)
	yielded := register(f.project, false)
	state(yielded, "yielded", "busy", ago("10 seconds"), false)
	stopped := register(f.project, false)
	state(stopped, "stopped", "busy", ago("10 seconds"), true)
	register(f.project, false) // registered, never heartbeated

	person := func(name string) tenant.Principal {
		t.Helper()
		p := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
		f.tx(t, p, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person',$3)`, p.TenantID, p.ID, name)
			return err
		})
		return p
	}
	bindProject := func(p tenant.Principal, role, project string) {
		t.Helper()
		if _, err := f.db.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1::uuid,$2::uuid,id,'project',$3::uuid FROM roles WHERE tenant_id=$1::uuid AND key=$4`, p.TenantID, p.ID, project, role); err != nil {
			t.Fatal(err)
		}
	}
	guest := person("guest on the first project")
	bindProject(guest, "guest", f.project)
	member := person("member of the second project")
	bindProject(member, "member", second)
	viewer := person("workspace viewer")
	if _, err := f.db.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1::uuid,$2::uuid,id,'workspace' FROM roles WHERE tenant_id=$1::uuid AND key='viewer'`, viewer.TenantID, viewer.ID); err != nil {
		t.Fatal(err)
	}
	reader := person("nodes reader")
	var roleID string
	if err := f.db.Admin.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,'nodes_reader','Nodes reader') RETURNING id::text`, reader.TenantID).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,'nodes.read')`, reader.TenantID, roleID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1::uuid,$2::uuid,$3::uuid,'workspace')`, reader.TenantID, reader.ID, roleID); err != nil {
		t.Fatal(err)
	}

	type item struct {
		harness.LiveAgent
		raw map[string]any
	}
	live := func(p tenant.Principal) []item {
		t.Helper()
		w := f.call(p, "GET", "/api/harness-sessions/live", nil, "")
		expect(t, w, 200)
		var page struct {
			Items        []json.RawMessage `json:"items"`
			At           string            `json:"at"`
			FreshSeconds int               `json:"fresh_seconds"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.At == "" || page.FreshSeconds != 120 {
			t.Fatalf("page clock %q fresh %d", page.At, page.FreshSeconds)
		}
		out := []item{}
		for _, raw := range page.Items {
			var v item
			if err := json.Unmarshal(raw, &v.LiveAgent); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &v.raw); err != nil {
				t.Fatal(err)
			}
			out = append(out, v)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ProjectID+out[i].Phase < out[j].ProjectID+out[j].Phase })
		return out
	}
	// Which (project, phase) pairs came back, and whether each names its agent.
	type seen struct {
		project, phase string
		named, linked  bool
	}
	summary := func(items []item) []seen {
		out := []seen{}
		for _, v := range items {
			_, hasName := v.raw["name"]
			_, hasPrincipal := v.raw["principal_id"]
			_, hasSession := v.raw["session_id"]
			if hasName != hasPrincipal {
				t.Fatalf("name and principal_id travel together: %v", v.raw)
			}
			out = append(out, seen{v.ProjectID, v.Phase, hasName, hasSession})
		}
		return out
	}
	want := func(name string, got []seen, expected ...seen) {
		t.Helper()
		sort.Slice(expected, func(i, j int) bool {
			return expected[i].project+expected[i].phase < expected[j].project+expected[j].phase
		})
		if len(got) != len(expected) {
			t.Fatalf("%s: got %+v want %+v", name, got, expected)
		}
		for i := range got {
			if got[i] != expected[i] {
				t.Fatalf("%s: got %+v want %+v", name, got, expected)
			}
		}
	}

	admin := live(f.person)
	want("admin", summary(admin), seen{f.project, "working", true, true}, seen{second, "starting", true, true})
	for _, v := range admin {
		if v.Phase == "working" {
			if v.ActivityNote == nil || *v.ActivityNote != "Reviewing project changes" { t.Fatalf("admin note %+v", v.LiveAgent) }
			if v.SessionID != working || v.PrincipalID != f.agent.ID || v.Name != "worker" || v.Harness != "claude" || v.Management != "unmanaged" || v.Activity != "busy" {
				t.Fatalf("working agent %+v", v.LiveAgent)
			}
			if v.Ticket == nil || v.Ticket.ID != f.ticket || v.Ticket.Key != "HTS-2" || v.Ticket.Title != "Harness ticket" || v.Ticket.ProjectID != f.project {
				t.Fatalf("ticket %+v", v.Ticket)
			}
			if v.Since.IsZero() || v.HeartbeatAt.IsZero() {
				t.Fatalf("times %+v", v.LiveAgent)
			}
		} else if v.SessionID != starting || v.Ticket != nil || v.raw["ticket"] != nil {
			t.Fatalf("starting agent %+v", v.raw)
		}
	}
	// A guest sees only its project, and that an agent works there, not which.
	want("guest", summary(live(guest)), seen{f.project, "working", false, false})
	if _, exposed := live(guest)[0].raw["activity_note"]; exposed { t.Fatal("guest read worker note") }
	if got := live(guest); got[0].Ticket == nil || got[0].Ticket.Key != "HTS-2" {
		t.Fatalf("guest ticket %+v", got[0].Ticket)
	}
	// A project member may know its agents' names, but the Agents workspace is not theirs.
	want("project member", summary(live(member)), seen{second, "starting", true, false})
	want("viewer", summary(live(viewer)), seen{f.project, "working", true, true}, seen{second, "starting", true, true})
	want("nodes reader", summary(live(reader)), seen{f.project, "working", false, false}, seen{second, "starting", false, false})
	for _, item := range live(reader) { if _, exposed := item.raw["activity_note"]; exposed { t.Fatal("nodes reader read worker note") } }
	if got := live(f.foreign); len(got) != 0 {
		t.Fatalf("tenant leak %+v", got)
	}
	// The agent's key reaches the route; the name needs a permission its scopes lack.
	want("agent", summary(live(f.agent)), seen{f.project, "working", false, false}, seen{second, "starting", false, false})

	// A ticket moved to the second project takes its agent along; a caller
	// who cannot see that project keeps the session, without the ticket.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.ticket, second)
		return err
	})
	want("admin after move", summary(live(f.person)), seen{f.project, "working", true, true}, seen{second, "starting", true, true}, seen{second, "working", true, true})
	// Both listings link the ticket where it lives now.
	for _, v := range live(f.person) {
		if v.Phase == "working" && (v.Ticket == nil || v.Ticket.ProjectID != second) {
			t.Fatalf("moved ticket keeps its old project %+v", v.Ticket)
		}
	}
	moved := live(guest)
	want("guest after move", summary(moved), seen{f.project, "working", false, false})
	if moved[0].Ticket != nil {
		t.Fatalf("an invisible ticket leaked %+v", moved[0].Ticket)
	}
	// The session itself belongs to the first project, which this member cannot see.
	want("member after move", summary(live(member)), seen{second, "starting", true, false})

	// One answer is bounded: the freshest heartbeats first, and it says so when it is cut.
	var page struct {
		Items     []harness.LiveAgent `json:"items"`
		Truncated bool                `json:"truncated"`
	}
	read := func() {
		t.Helper()
		w := f.call(f.person, "GET", "/api/harness-sessions/live", nil, "")
		expect(t, w, 200)
		page.Items, page.Truncated = nil, false
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if page.Truncated {
		t.Fatal("two live sessions reported as truncated")
	}
	restore := harness.SetMaxLive(1)
	read()
	restore()
	if !page.Truncated || len(page.Items) != 1 || page.Items[0].SessionID != starting {
		t.Fatalf("bounded answer %+v truncated=%v", page.Items, page.Truncated)
	}

	expect(t, f.call(tenant.Principal{}, "GET", "/api/harness-sessions/live", nil, ""), 401)
	f.key = "invalid"
	expect(t, f.call(f.agent, "GET", "/api/harness-sessions/live", nil, ""), 403)
}

// The live read walks its partial index from the freshest heartbeat down to the
// freshness window, under the caller's row-level security, instead of sorting
// every open session.
func TestLiveAgentsUseTheLiveIndex(t *testing.T) {
	f := fixture(t)
	for _, p := range []tenant.Principal{f.person, f.agent} {
		f.tx(t, p, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `SET LOCAL enable_seqscan=off`); err != nil {
				return err
			}
			rows, err := tx.Query(t.Context(), `EXPLAIN `+harness.LiveQuery, 120.0, 501)
			if err != nil {
				return err
			}
			defer rows.Close()
			plan := ""
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					return err
				}
				plan += line + "\n"
			}
			if err := rows.Err(); err != nil {
				return err
			}
			bounded := false
			for _, line := range strings.Split(plan, "\n") {
				bounded = bounded || strings.Contains(line, "Index Cond:") && strings.Contains(line, "heartbeat_at >")
			}
			if !strings.Contains(plan, "Index Scan using harness_sessions_live_idx") || !bounded || strings.Contains(plan, "Sort") {
				t.Fatalf("live read does not walk its index:\n%s", plan)
			}
			return nil
		})
	}
}
