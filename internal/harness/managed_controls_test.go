// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"encoding/json"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/jackc/pgx/v5"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagedControlsIdentityReplayPrivacyAndExpiry(t *testing.T) {
	f := fixture(t)
	order, run := stateRun(t, f, f.project, "running", "MCT-10")
	lease := "managed-controls-lease-00000000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "fixture", "harness_session_ref": "managed-controls-ref-0000000000001", "worker_lease": lease, "management_mode": "managed", "role": "worker", "run_id": run, "work_order_id": order, "ticket_node_id": order, "work_shape": "ship", "advertised_capabilities": []string{"managed_control_v1", "steer", "interrupt", "stop"}}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	identity := ownedprocess.Identity{DaemonID: "fixture", Generation: strings.Repeat("a", 32), ProcessID: strings.Repeat("b", 32), RootPID: 1234, GroupID: 1234, StartedAt: time.Now().UTC().Truncate(time.Second)}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET daemon_id=$2,daemon_generation=$3 WHERE id=$1`, run, identity.DaemonID, identity.Generation)
		return err
	})
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "process_ownership": identity}, lease), 200)
	body := map[string]any{"request_id": uid(), "kind": "steer", "text": "private steer fixture", "expected_ownership": identity}
	expect(t, f.call(f.agent, "POST", path+"/managed-controls", body, lease), 403)
	expect(t, f.call(f.person, "POST", path+"/controls/interrupt", map[string]any{}, ""), 409)
	wrong := identity
	wrong.ProcessID = strings.Repeat("c", 32)
	body["expected_ownership"] = wrong
	expect(t, f.call(f.person, "POST", path+"/managed-controls", body, ""), 409)
	body["expected_ownership"] = identity
	// A live managed session cannot qualify by advertising a capability for
	// an unsupported harness. Restore Claude before checking the happy path.
	for _, adapter := range []string{"codex", "cursor", "pi", "grok"} {
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET harness=$2 WHERE id=$1`, id, adapter)
			return err
		})
		expect(t, f.call(f.person, "POST", path+"/managed-controls", body, ""), 409)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET harness='claude' WHERE id=$1`, id)
		return err
	})
	w = f.call(f.person, "POST", path+"/managed-controls", body, "")
	expect(t, w, 201)
	control := decode(t, w)["id"].(string)
	if strings.Contains(w.Body.String(), "private steer") {
		t.Fatal("text in public response")
	}
	expect(t, f.call(f.person, "POST", path+"/managed-controls", body, ""), 201)
	body["text"] = "divergent"
	expect(t, f.call(f.person, "POST", path+"/managed-controls", body, ""), 409)
	dbtest.BindRole(t, f.db, f.foreign.TenantID, f.foreign.ID, "admin")
	expect(t, f.call(f.foreign, "POST", path+"/managed-controls", body, ""), 404)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "viewer")
	expect(t, f.call(f.person, "POST", path+"/managed-controls", body, ""), 403)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	controls := decode(t, w)["controls"].([]any)
	if len(controls) != 1 || controls[0].(map[string]any)["text"] != "private steer fixture" {
		t.Fatal(w.Body.String())
	}
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	if len(decode(t, w)["controls"].([]any)) != 0 {
		t.Fatal("claimed steer replayed")
	}
	expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", map[string]any{"outcome": "applied", "reason": "queued_next_turn"}, lease), 200)
	body["request_id"] = uid()
	body["text"] = "expire me"
	w = f.call(f.person, "POST", path+"/managed-controls", body, "")
	expect(t, w, 201)
	expired := decode(t, w)["id"].(string)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_controls SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, expired)
		return err
	})
	w = f.call(f.person, "GET", path+"/controls/"+expired, nil, "")
	expect(t, w, 200)
	if decode(t, w)["reason"] != "authorization_expired" {
		t.Fatal(w.Body.String())
	}

	// A revoked sender is checked again at claim, not only enqueue.
	body["request_id"] = uid()
	body["text"] = "revoked fixture"
	w = f.call(f.person, "POST", path+"/managed-controls", body, "")
	expect(t, w, 201)
	revoked := decode(t, w)["id"].(string)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "viewer")
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	if len(decode(t, w)["controls"].([]any)) != 0 {
		t.Fatal("revoked control delivered")
	}
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	w = f.call(f.person, "GET", path+"/controls/"+revoked, nil, "")
	expect(t, w, 200)
	if decode(t, w)["reason"] != "authorization_revoked" {
		t.Fatal(w.Body.String())
	}
	// Restart loses only transient text; it never manufactures an applied result.
	body["request_id"] = uid()
	body["text"] = "restart fixture"
	w = f.call(f.person, "POST", path+"/managed-controls", body, "")
	expect(t, w, 201)
	lost := decode(t, w)["id"].(string)
	f.mux = http.NewServeMux()
	harness.New(f.db.App).Mount(f.mux)
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	if len(decode(t, w)["controls"].([]any)) != 0 {
		t.Fatal("lost text replayed")
	}
	w = f.call(f.person, "GET", path+"/controls/"+lost, nil, "")
	expect(t, w, 200)
	if decode(t, w)["reason"] != "transient_input_unavailable" {
		t.Fatal(w.Body.String())
	}
	// Merged context uses row bindings; request selectors are refused.
	f.agent.KeyCreatorID = f.person.ID
	rules.New(f.db.App).Mount(f.mux)
	w = f.call(f.person, "POST", "/api/rules/layers", map[string]any{"layer": "company"}, "")
	expect(t, w, 200)
	layer := decode(t, w)["id"]
	w = f.call(f.person, "POST", "/api/rules/sets", map[string]any{"layer_id": layer, "name": "Safety"}, "")
	expect(t, w, 200)
	setID := decode(t, w)["id"].(string)
	rule := rules.Rule{Identity: "managed-safety", Text: "Keep credentials private.", Why: "Safety.", Strength: "locked", Enabled: true, Source: rules.Source{Reference: "AEON-260"}}
	expect(t, f.call(f.person, "PUT", "/api/rules/sets/"+setID+"/draft", map[string]any{"expected_revision": 1, "name": "Safety", "rules": []rules.Rule{rule}}, ""), 200)
	expect(t, f.call(f.person, "POST", "/api/rules/sets/"+setID+"/publish", map[string]any{"expected_revision": 2, "version": "260929000000.0.0"}, ""), 200)
	w = f.call(f.agent, "POST", path+"/managed-context", map[string]any{}, lease)
	expect(t, w, 200)
	var merged rules.Merged
	if err := json.Unmarshal(w.Body.Bytes(), &merged); err != nil {
		t.Fatal(err)
	}
	if err := rules.ValidateMerged(merged, merged.Context, time.Now()); err != nil {
		t.Fatal(err)
	}
	if merged.Context.PersonID != f.person.ID || merged.Context.TaskID != order || merged.Context.ProjectID != f.project || merged.Context.Harness != "claude-code" {
		t.Fatal("wrong context")
	}
	expect(t, f.call(f.agent, "POST", path+"/managed-context", map[string]any{"person_id": f.foreign.ID}, lease), 400)
	expect(t, f.call(f.agent, "POST", path+"/managed-context", map[string]any{}, "wrong-lease"), 403)
	// The shared workorders endpoint must retain the rules error status/code.
	const repository = "inspr-at/managed-doctrine"
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO doctrine_sources(tenant_id,repository,visibility,ref,commit_sha,paths,credential_ref) VALUES($1,$2,'public','fixture',$3,ARRAY['docs/AGENTS-KERNEL.md'],'managed-read')`, f.person.TenantID, repository, strings.Repeat("a", 40))
		return err
	})
	creds := t.TempDir()
	grant := func(repository string) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"grants": []any{map[string]string{"tenant_id": f.person.TenantID, "repository": repository}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(creds, "managed-read.allowlist.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plainMux := f.mux
	withCredentials := func(dir string) {
		f.mux = http.NewServeMux()
		f.mux.Handle("/", (doctrine.Credentials{Dir: dir}).CatalogMiddleware(plainMux))
	}
	grant(repository)
	withCredentials(creds)
	expect(t, f.call(f.agent, "POST", path+"/managed-context", map[string]any{}, lease), 200)
	for _, mode := range []string{"revoked", "missing directory", "missing middleware"} {
		switch mode {
		case "revoked":
			grant("other/repository")
		case "missing directory":
			grant(repository)
			withCredentials(filepath.Join(creds, "missing"))
		case "missing middleware":
			f.mux = plainMux
		}
		w = f.call(f.agent, "POST", path+"/managed-context", map[string]any{}, lease)
		expect(t, w, 503)
		if decode(t, w)["code"] != "doctrine_unavailable" || strings.Contains(w.Body.String(), repository) || strings.Contains(w.Body.String(), creds) {
			t.Fatalf("%s: unsafe or unexplained doctrine failure: %s", mode, w.Body.String())
		}
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var leaked bool
		err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM events WHERE coalesce("before"::text,'') LIKE '%private steer%' OR coalesce("after"::text,'') LIKE '%private steer%')`).Scan(&leaked)
		if leaked {
			t.Fatal("steer in events")
		}
		return err
	})
}

func TestManagedControlFreshnessUsesDatabaseClock(t *testing.T) {
	// Same contract as force-stop freshness: the observation window is the
	// database clock, including when that clock is nowhere near the host wall clock.
	now := time.Date(2099, time.January, 1, 11, 0, 0, 0, time.UTC)
	f := fixtureWithOwnershipClock(t, func() time.Time { return now })
	order, run := stateRun(t, f, f.project, "running", "MCT-11")
	lease := "managed-clock-lease-000000000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "fixture", "harness_session_ref": "managed-clock-ref-0000000000000001", "worker_lease": lease, "management_mode": "managed", "role": "worker", "run_id": run, "work_order_id": order, "ticket_node_id": order, "work_shape": "ship", "advertised_capabilities": []string{"managed_control_v1", "steer", "stop"}}, "")
	expect(t, w, 201)
	path := base + "/" + decode(t, w)["id"].(string)
	identity := ownedprocess.Identity{DaemonID: "fixture", Generation: strings.Repeat("a", 32), ProcessID: strings.Repeat("b", 32), RootPID: 1234, GroupID: 1234, StartedAt: now.Add(-time.Hour)}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET daemon_id=$2,daemon_generation=$3 WHERE id=$1`, run, identity.DaemonID, identity.Generation)
		return err
	})
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "process_ownership": identity}, lease), 200)
	body := map[string]any{"request_id": uid(), "kind": "steer", "text": "clock fixture", "expected_ownership": identity}
	w = f.call(f.person, "POST", path+"/managed-controls", body, "")
	expect(t, w, 201)
	if strings.Contains(w.Body.String(), "clock fixture") {
		t.Fatal("text in public response")
	}
	now = now.Add(46 * time.Second)
	body["request_id"] = uid()
	w = f.call(f.person, "POST", path+"/managed-controls", body, "")
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "live sandboxed managed control unavailable") {
		t.Fatal(w.Body.String())
	}
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 2, "process_ownership": identity}, lease), 200)
	body["request_id"] = uid()
	expect(t, f.call(f.person, "POST", path+"/managed-controls", body, ""), 201)
}

func TestManagedControlsRefuseUnmanagedAndUnknownPolicy(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "unmanaged-controls-lease-0000000000001"
	body := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "fixture", "harness_session_ref": "unmanaged-control-ref-000000000001", "worker_lease": lease, "management_mode": "unmanaged", "role": "worker"}
	w := f.call(f.person, "POST", base, body, "")
	expect(t, w, 201)
	path := base + "/" + decode(t, w)["id"].(string)
	w = f.call(f.person, "POST", path+"/managed-controls", map[string]any{"request_id": uid(), "kind": "stop", "expected_ownership": map[string]any{}}, "")
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "unmanaged") {
		t.Fatal("missing explanation")
	}
	expect(t, f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease), 409)
	expect(t, f.call(f.agent, "POST", path+"/drain", map[string]any{}, lease), 409)
	expect(t, f.call(f.agent, "POST", path+"/managed-context", map[string]any{}, lease), 409)
	body["advertised_capabilities"] = []string{"managed_control_v1"}
	expect(t, f.call(f.person, "POST", base, body, ""), 400)
}
