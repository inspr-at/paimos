// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestRulesClientReportsAreOptionalLeaseBoundAndDowngradeSafe(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "client-capability-lease-00000000000001"
	in := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "client-test", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": "client-capability-ref-000000000001", "worker_lease": lease, "max_session_file_bytes": 64000, "rules_client_version": "260929120000.0.0"}
	in["max_session_file_bytes"] = rules.MaxBytes + 1
	expect(t, f.call(f.agent, "POST", base, in, ""), 400)
	in["max_session_file_bytes"] = 64000
	w := f.call(f.agent, "POST", base, in, "")
	expect(t, w, 201)
	if strings.Contains(w.Body.String(), "max_session_file_bytes") || strings.Contains(w.Body.String(), "rules_client_version") {
		t.Fatal("strict session response contract changed")
	}
	id := decode(t, w)["id"].(string)
	assertStored := func(want *int) {
		t.Helper()
		f.tx(t, f.person, func(tx pgx.Tx) error {
			var got *int
			err := tx.QueryRow(t.Context(), `SELECT max_session_file_bytes FROM harness_sessions WHERE id=$1`, id).Scan(&got)
			if (got == nil) != (want == nil) || (got != nil && *got != *want) {
				t.Fatalf("stored capability: %v want %v", got, want)
			}
			return err
		})
	}
	maximum := 64000
	assertStored(&maximum)
	expect(t, f.call(f.agent, "POST", base, in, ""), 201) // idempotent registration still reports
	beat := map[string]any{"phase": "working", "activity_sequence": 1, "max_session_file_bytes": 12000, "rules_client_version": "old-client"}
	expect(t, f.call(f.agent, "POST", base+"/"+id+"/heartbeat", beat, "wrong-worker-lease-00000000000000"), 403)
	assertStored(&maximum)
	expect(t, f.call(f.agent, "POST", base+"/"+id+"/heartbeat", beat, lease), 200)
	maximum = 12000
	assertStored(&maximum)
	beat["max_session_file_bytes"] = 64000
	expect(t, f.call(f.agent, "POST", base+"/"+id+"/heartbeat", beat, lease), 200)
	maximum = 64000
	assertStored(&maximum)
	delete(beat, "max_session_file_bytes")
	delete(beat, "rules_client_version")
	w = f.call(f.agent, "POST", base+"/"+id+"/heartbeat", beat, lease)
	expect(t, w, 200)
	assertStored(nil) // a rollback cannot keep an earlier 64 KB claim
	if strings.Contains(w.Body.String(), "max_session_file_bytes") {
		t.Fatal("heartbeat response changed")
	}
	for _, bad := range []any{1999, rules.MaxBytes + 1, "64000", -1} {
		beat["max_session_file_bytes"] = bad
		expect(t, f.call(f.agent, "POST", base+"/"+id+"/heartbeat", beat, lease), 400)
		assertStored(nil)
	}
}

func TestManagedRulesRespectRequestAndRegisteredCeilings(t *testing.T) {
	f := fixture(t)
	f.agent.KeyCreatorID = f.person.ID
	order, run := stateRun(t, f, f.project, "running", "RCB-1")
	lease := "managed-rules-capability-lease-000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "rules-test", "management_mode": "managed", "role": "worker", "harness_session_ref": "managed-rules-capability-ref-000001", "worker_lease": lease, "run_id": run, "work_order_id": order, "ticket_node_id": order, "work_shape": "ship", "max_session_file_bytes": rules.MaxBytes}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	rules.New(f.db.App).Mount(f.mux)
	expect(t, f.call(f.person, "PUT", "/api/rules/budget", map[string]any{"max_bytes": rules.MaxBudgetBytes}, ""), 200)
	w = f.call(f.person, "POST", "/api/rules/layers", map[string]any{"layer": "company"}, "")
	expect(t, w, 200)
	layer := decode(t, w)["id"]
	floor := rules.Rule{Identity: "safe", Text: "Keep safety.", Why: "Safety.", Strength: "locked", Enabled: true, Source: rules.Source{Reference: "fixture"}}
	lines := []rules.Rule{floor}
	remaining := rules.MaxBudgetBytes - len(rules.RenderedBody(lines))
	for i := 0; i < 1250; i++ {
		r := rules.Rule{Identity: fmt.Sprintf("rule-%03d", i), Why: "Fixture.", Strength: "normal", Enabled: true, Source: rules.Source{Reference: "fixture"}}
		remaining -= len("- [" + r.Identity + "] \n")
		lines = append(lines, r)
	}
	for i := 1; i < len(lines); i++ {
		n := remaining / (len(lines) - i)
		lines[i].Text = strings.Repeat("<", n)
		remaining -= n
	}
	for offset := 0; offset < len(lines); offset += 100 {
		w = f.call(f.person, "POST", "/api/rules/sets", map[string]any{"layer_id": layer, "name": fmt.Sprintf("Rules %d", offset)}, "")
		expect(t, w, 200)
		setID := decode(t, w)["id"].(string)
		expect(t, f.call(f.person, "PUT", "/api/rules/sets/"+setID+"/draft", map[string]any{"expected_revision": 1, "name": "Rules", "rules": lines[offset:min(offset+100, len(lines))]}, ""), 200)
		expect(t, f.call(f.person, "POST", "/api/rules/sets/"+setID+"/publish", map[string]any{"expected_revision": 2, "version": fmt.Sprintf("2609291200%02d.0.0", offset/100)}, ""), 200)
	}
	request := func(limit string) rules.Merged {
		t.Helper()
		req := httptest.NewRequest("POST", base+"/"+id+"/managed-context", bytes.NewBufferString("{}"))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), f.agent))
		req.Header.Set("Authorization", "Bearer "+f.key)
		req.Header.Set("X-Aeon-Worker-Lease", lease)
		if limit != "" {
			req.Header.Set(rules.ClientMaximumHeader, limit)
		}
		out := httptest.NewRecorder()
		f.mux.ServeHTTP(out, req)
		expect(t, out, 200)
		var m rules.Merged
		if err := json.Unmarshal(out.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := request("512000"); m.ByteSize != rules.MaxBudgetBytes || strings.Contains(m.Body, "Compatibility cut") {
		t.Fatal("full managed boundary not delivered", m.ByteSize)
	}
	for _, limit := range []string{"", "12000"} {
		if m := request(limit); m.ByteSize > 12000 || !strings.Contains(m.Body, "Compatibility cut") {
			t.Fatal("legacy managed limit lost")
		}
	}
	// A new request header cannot override a session last reported by an old daemon.
	expect(t, f.call(f.agent, "POST", base+"/"+id+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1}, lease), 200)
	if m := request("64000"); m.ByteSize > 12000 || !strings.Contains(m.Body, "Compatibility cut") {
		t.Fatal("registered maximum ignored")
	}
}
