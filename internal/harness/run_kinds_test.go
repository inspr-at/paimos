// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestRunKindsRegisterBeatAndAggregate(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	leadLease := "coordinator-lease-" + uid()
	lead := f.registerSession(t, f.agent.ID, "coordinator", f.ticket, "coordinator-ref-"+uid(), leadLease)
	for _, tc := range []struct {
		harness, field, label string
		progress              int
	}{
		{"codex", "model", "gpt-fixture", 20}, {"media", "generator", "higgsfield/kling3_0", 40}, {"terminal", "command", "ffmpeg", 90},
	} {
		lease := "child-lease-" + uid()
		body := map[string]any{"agent_principal_id": f.agent.ID, "harness": tc.harness, "host": "mbp2606", "role": "worker", "management_mode": "unmanaged", "parent_harness_session_id": lead, "ticket_node_id": f.ticket, "work_shape": "ship", "harness_session_ref": "child-ref-" + uid(), "worker_lease": lease, tc.field: tc.label}
		w := f.call(f.agent, "POST", base, body, "")
		expect(t, w, 201)
		child := decode(t, w)
		id := child["id"].(string)
		if child[tc.field] != tc.label || child["parent_harness_session_id"] != lead {
			t.Fatalf("wrong child: %#v", child)
		}
		expect(t, f.call(f.agent, "POST", base, body, ""), 201)
		if tc.harness == "media" {
			body[tc.field] = "higgsfield/veo3_1"
			expect(t, f.call(f.agent, "POST", base, body, ""), 409)
		}
		beat := f.beat(t, id, lease, 1, map[string]any{"progress_pct": tc.progress, "eta_ready_at": time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)})
		if beat[tc.field] != tc.label || beat["progress_pct"] != float64(tc.progress) {
			t.Fatalf("wrong beat: %#v", beat)
		}
		if tc.harness != "codex" {
			expect(t, f.call(f.agent, "POST", base+"/"+id+"/heartbeat", map[string]any{"phase": "working", "model": "claude-fiction"}, lease), 400)
		}
	}
	f.beat(t, lead, leadLease, 1, map[string]any{"eta_live_at": time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339)})
	// Read only the coordinator: children are outside the page but still count.
	cursor := ""
	found := false
	for i := 0; i < 4; i++ {
		page := decode(t, f.call(f.person, "GET", "/api/harness-sessions?limit=1&state=working"+cursor, nil, ""))
		for _, raw := range page["items"].([]any) {
			item := raw.(map[string]any)
			if item["id"] == lead {
				found = true
				if item["progress_pct"] != float64(50) {
					t.Fatal("pagination lost child progress")
				}
			}
		}
		if page["next_cursor"] == nil {
			break
		}
		cursor = "&cursor=" + page["next_cursor"].(string)
	}
	if !found {
		t.Fatal("coordinator missing from pages")
	}
	detail := decode(t, f.call(f.person, "GET", base+"/"+lead, nil, ""))
	if detail["progress_pct"] != float64(50) {
		t.Fatalf("want mean 50, got %#v", detail)
	}
	for _, field := range []string{"progress_pct", "eta_ready_at"} {
		value := any(25)
		if field == "eta_ready_at" {
			value = time.Now().UTC().Format(time.RFC3339)
		}
		w := f.call(f.agent, "POST", base+"/"+lead+"/heartbeat", map[string]any{"phase": "working", field: value}, leadLease)
		expect(t, w, 400)
		if !strings.Contains(w.Body.String(), "--progress") || !strings.Contains(w.Body.String(), "--eta-ready") || !strings.Contains(w.Body.String(), "derived") {
			t.Fatal("imprecise coordinator error")
		}
	}
}

func TestProcessRunValidation(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	lead := f.registerSession(t, f.agent.ID, "coordinator", f.ticket, "lead-ref-"+uid(), "lead-lease-"+uid())
	for _, change := range []map[string]any{
		{"generator": ""}, {"generator": strings.Repeat("x", 121)}, {"generator": "veo\n3"}, {"generator": "veo<script>"},
		{"parent_harness_session_id": nil}, {"ticket_node_id": nil}, {"role": "coordinator"}, {"model": "claude"}, {"command": "ffmpeg"},
	} {
		body := map[string]any{"agent_principal_id": f.agent.ID, "harness": "media", "generator": "higgsfield/kling3_0", "host": "mbp2606", "role": "worker", "management_mode": "unmanaged", "parent_harness_session_id": lead, "ticket_node_id": f.ticket, "work_shape": "ship", "harness_session_ref": "invalid-ref-" + uid(), "worker_lease": "invalid-lease-" + uid()}
		for k, v := range change {
			body[k] = v
		}
		expect(t, f.call(f.agent, "POST", base, body, ""), 400)
	}
}

func TestCoordinatorProgressCountsMissingAndFinishedWorkers(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	lead := f.registerSession(t, f.agent.ID, "coordinator", f.ticket, "aggregate-ref-"+uid(), "aggregate-lease-"+uid())
	for _, tc := range []struct {
		progress         any
		stopped, removed bool
	}{
		{60, false, false}, {nil, false, false}, {100, true, false}, {90, true, false}, {100, false, true},
	} {
		lease := "worker-lease-" + uid()
		child := f.registerSession(t, f.agent.ID, "worker", f.ticket, "worker-ref-"+uid(), lease)
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET parent_id=$2,progress_pct=$3 WHERE id=$1`, child, lead, tc.progress)
			return err
		})
		if tc.stopped {
			expect(t, f.call(f.agent, "POST", base+"/"+child+"/stop", map[string]any{"reason": "process_exited"}, lease), 200)
		}
		if tc.removed {
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET archived_at=clock_timestamp() WHERE id=$1`, child)
				return err
			})
		}
	}
	detail := decode(t, f.call(f.person, "GET", base+"/"+lead, nil, ""))
	if detail["progress_pct"] != float64(53) {
		t.Fatalf("missing or ended worker counted incorrectly: %#v", detail)
	}
}

func TestHostLabelsArePersonOwnedAndResettable(t *testing.T) {
	f := fixture(t)
	f.registerSession(t, f.agent.ID, "worker", f.ticket, "host-ref-"+uid(), "host-lease-"+uid())
	other := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','other')`, other.TenantID, other.ID)
		return err
	})
	dbtest.BindRole(t, f.db, other.TenantID, other.ID, "admin")
	path := "/api/me/host-labels"
	put := func(p tenant.Principal, label any) {
		t.Helper()
		expect(t, f.call(p, "PUT", path, map[string]any{"host": "build-host", "label": label}, ""), 200)
	}
	put(f.person, "David's MacBook")
	put(other, "Test computer")
	for _, tc := range []struct {
		p     tenant.Principal
		label string
	}{{f.person, "David's MacBook"}, {other, "Test computer"}} {
		w := f.call(tc.p, "GET", path, nil, "")
		expect(t, w, 200)
		var labels []struct{ Host, Label string }
		if err := json.Unmarshal(w.Body.Bytes(), &labels); err != nil {
			t.Fatal(err)
		}
		if len(labels) != 1 || labels[0].Label != tc.label {
			t.Fatalf("leaked labels: %#v", labels)
		}
	}
	// Exercise the database fence independently of the endpoint's owner filter.
	for _, p := range []tenant.Principal{other, f.agent, f.foreign} {
		err := db.InTenant(tenant.WithPrincipal(t.Context(), p), f.db.App, p.TenantID, func(tx pgx.Tx) error {
			var count int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM person_host_labels WHERE person_id=$1`, f.person.ID).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Error("RLS exposed another person's label")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	put(f.person, nil)
	w := f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal("reset left an override")
	}
	expect(t, f.call(f.agent, "GET", path, nil, ""), 403)
	expect(t, f.call(f.agent, "PUT", path, map[string]any{"host": "build-host", "label": "agent"}, ""), 403)
	expect(t, f.call(f.person, "PUT", path, map[string]any{"host": "unknown", "label": "test"}, ""), 404)
	for _, label := range []string{"", strings.Repeat("a", 129), "bad\nname"} {
		expect(t, f.call(f.person, "PUT", path, map[string]any{"host": "build-host", "label": label}, ""), 400)
	}
}
