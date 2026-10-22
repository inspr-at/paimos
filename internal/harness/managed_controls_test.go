// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"encoding/json"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workerlease"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Risk: revoking the run person prevents an owned stop, or a safety exception
// leaks task/steer content and admits unrelated worker mutations.
func TestRunPersonRevocationKeepsOnlyOwnedSafetyControls(t *testing.T) {
	for _, revoke := range []string{"deactivation", "project_access"} {
		t.Run(revoke, func(t *testing.T) {
			f := fixture(t)
			order, run := stateRun(t, f, f.project, "running", "SAFETY-10")
			lease := "safety-controls-worker-lease-00000000001"
			base := "/api/projects/" + f.project + "/harness-sessions"
			w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "private-host", "harness_session_ref": "safety-controls-ref-0000000000001", "worker_lease": lease, "management_mode": "managed", "role": "worker", "run_id": run, "work_order_id": order, "ticket_node_id": order, "work_shape": "ship", "advertised_capabilities": []string{"managed_control_v1", "steer", "stop"}}, "")
			expect(t, w, 201)
			id := decode(t, w)["id"].(string)
			path := base + "/" + id
			identity := ownedprocess.Identity{DaemonID: "fixture", Generation: strings.Repeat("a", 32), ProcessID: strings.Repeat("b", 32), RootPID: 1234, GroupID: 1234, StartedAt: time.Now().UTC().Truncate(time.Second)}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				if err := agentruns.SetActingPersonTx(t.Context(), tx, f.person.TenantID, run, f.person.ID); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET daemon_id=$2,daemon_generation=$3 WHERE id=$1`, run, identity.DaemonID, identity.Generation)
				return err
			})
			expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "process_ownership": identity, "doing": "Reviewing task content"}, lease), 200)
			controls := map[string]string{}
			for _, kind := range []string{"steer", "stop"} {
				body := map[string]any{"request_id": uid(), "kind": kind, "expected_ownership": identity}
				if kind == "steer" {
					body["text"] = "private-steer-content"
				}
				w = f.call(f.person, "POST", path+"/managed-controls", body, "")
				expect(t, w, 201)
				controls[kind] = decode(t, w)["id"].(string)
			}
			// Keep the independently authenticated computer transport active.
			// The run's starting person, never its key issuer, is revoked.
			var admin string
			keyParts := strings.SplitN(f.key, "_", 3)
			prefix := strings.ReplaceAll(f.person.TenantID, "-", "") + keyParts[1][:16]
			f.tx(t, f.person, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Retained transport administrator') RETURNING id::text`, f.person.TenantID).Scan(&admin); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET created_by_principal_id=$2,prefix=$3 WHERE id=$1`, f.agent.AuthKeyID, admin, prefix)
				return err
			})
			f.key = "aeon_" + prefix + "_" + keyParts[2]
			dbtest.BindRole(t, f.db, f.person.TenantID, admin, "admin")
			f.agent.KeyID, f.agent.KeyCreatorID = f.agent.AuthKeyID, admin
			am, err := auth.New(auth.Config{Env: "dev", PublicURL: "https://safety.test", SessionKey: []byte(strings.Repeat("f", 32))}, f.db.App)
			if err != nil {
				t.Fatal(err)
			}
			handler := (&httpapi.Server{Mux: f.mux, Pool: f.db.App, Middleware: []func(http.Handler) http.Handler{am.Middleware}}).Handler()
			workerCall := func(target string, body any, proof string) *httptest.ResponseRecorder {
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest("POST", target, strings.NewReader(string(raw)))
				workerlease.Install(r.Header, f.key, proof)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				query := `UPDATE principals SET status='deactivated' WHERE id=$1`
				if revoke == "project_access" {
					query = `DELETE FROM role_bindings WHERE principal_id=$1`
				}
				_, err := tx.Exec(t.Context(), query, f.person.ID)
				return err
			})
			expect(t, workerCall(path+"/yield", map[string]any{}, "wrong-lease-with-at-least-32-characters"), 403)
			expect(t, workerCall(base+"/"+uid()+"/yield", map[string]any{}, lease), 403)
			w = workerCall(path+"/yield", map[string]any{}, lease)
			expect(t, w, 200)
			result := decode(t, w)
			offered := result["controls"].([]any)
			if len(offered) != 1 || offered[0].(map[string]any)["id"] != controls["stop"] {
				t.Fatalf("safety yield offered non-stop work: %s", w.Body.String())
			}
			for _, forbidden := range []string{"private-", "Reviewing task content", "ticket_node_id", "work_order_id", "model", "handover", "host"} {
				if strings.Contains(w.Body.String(), forbidden) {
					t.Fatalf("safety response exposed %s: %s", forbidden, w.Body.String())
				}
			}
			expect(t, workerCall(path+"/managed-context", map[string]any{}, lease), 403)
			expect(t, workerCall(path+"/controls/"+controls["steer"]+"/complete", map[string]any{"outcome": "applied", "reason": "agentd_applied"}, lease), 403)
			expect(t, workerCall(path+"/controls/"+controls["stop"]+"/complete", map[string]any{"outcome": "applied", "reason": "native_close_exited"}, lease), 200)
			expect(t, workerCall(path+"/stop", map[string]any{"reason": "paused", "handover": map[string]any{}}, lease), 403)
			w = workerCall(path+"/stop", map[string]any{"reason": "process_exited"}, lease)
			expect(t, w, 200)
			if strings.Contains(w.Body.String(), "private-") || len(decode(t, w)) != 5 {
				t.Fatalf("stop returned task content: %s", w.Body.String())
			}
			expect(t, workerCall(path+"/confirm-exit", map[string]any{"reason": "process_exited"}, lease), 200)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var stops, leaks int
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE type='harness.stopped' AND actor_principal_id=$2),count(*) FILTER(WHERE type IN ('harness.yielded','harness.stopped','harness.control_claimed','harness.control_completed') AND (before::text LIKE '%private-%' OR after::text LIKE '%private-%')) FROM events WHERE after->>'id'=$1 OR before->>'id'=$1`, id, f.agent.ID).Scan(&stops, &leaks); err != nil {
					return err
				}
				if stops != 1 || leaks != 0 {
					t.Fatalf("safety audit lost actor or included content: stops=%d leaks=%d", stops, leaks)
				}
				return nil
			})
		})
	}
}

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
	if len(controls) != 1 || controls[0].(map[string]any)["text"] != "private steer fixture" || controls[0].(map[string]any)["author_principal_id"] != f.person.ID {
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

// Risk: an opted-in daemon must fail closed before launching with a missing,
// partial or oversized pin; legacy requests keep their existing semantics.
func TestManagedPinnedContextRefusesMissingAndOversizedPins(t *testing.T) {
	f := fixture(t)
	order, run := stateRun(t, f, f.project, "running", "MPC-10")
	f.agent.KeyCreatorID = f.person.ID
	lease := "pinned-context-lease-0000000000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "fixture", "harness_session_ref": "pinned-context-ref-000000000000001", "worker_lease": lease, "management_mode": "managed", "role": "worker", "run_id": run, "work_order_id": order, "ticket_node_id": order, "work_shape": "ship"}, "")
	expect(t, w, 201)
	endpoint := base + "/" + decode(t, w)["id"].(string) + "/managed-context"
	request := func(status int, code string) {
		t.Helper()
		w = f.call(f.agent, "POST", endpoint, map[string]bool{"pinned_files": true}, lease)
		expect(t, w, status)
		if code != "" && decode(t, w)["code"] != code {
			t.Fatalf("wrong refusal: %s", w.Body.String())
		}
	}
	request(409, "rules_pin_missing")
	var source string
	commit := strings.Repeat("a", 40)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO doctrine_sources(tenant_id,repository,visibility,commit_sha,paths) VALUES($1,'fixture/company','public',$2,ARRAY['docs/AGENTS-KERNEL.md']) RETURNING id::text`, f.person.TenantID, commit).Scan(&source)
	})
	request(409, "rules_pin_missing")
	content := []byte(strings.Repeat("R", 27603))
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO doctrine_cache(tenant_id,source_id,commit_sha,path,blob_sha,content) VALUES($1,$2,$3,'docs/AGENTS-KERNEL.md',$4,$5)`, f.person.TenantID, source, commit, doctrine.BlobSHA(content), content); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE doctrine_sources SET indexed_at=clock_timestamp() WHERE id=$1`, source)
		return err
	})
	request(200, "")
	var got rules.Merged
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.PinnedFiles) != 1 || got.PinnedFiles[0].Content != string(content) {
		t.Fatal("measured pin not delivered")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE doctrine_cache SET content=$2,blob_sha=$3 WHERE source_id=$1`, source, []byte(strings.Repeat("R", 64001)), doctrine.BlobSHA([]byte(strings.Repeat("R", 64001))))
		return err
	})
	request(422, "rules_pack_too_large")
}

// Risk: the real docs/AGENTS-*.md default includes a >64 KB core and >128 KB
// total. Only the always-on company kernels may enter a launch pack; a person's
// explicitly indexed profile and role/on-demand packs must never leak into it.
func TestManagedPinnedContextDefaultDoctrineSelectsKernels(t *testing.T) {
	f := fixture(t)
	order, run := stateRun(t, f, f.project, "running", "KERNEL-10")
	f.agent.KeyCreatorID = f.person.ID
	lease := "kernel-context-lease-0000000000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "fixture", "harness_session_ref": "kernel-context-ref-000000000000001", "worker_lease": lease, "management_mode": "managed", "role": "worker", "run_id": run, "work_order_id": order, "ticket_node_id": order, "work_shape": "ship"}, "")
	expect(t, w, 201)
	endpoint := base + "/" + decode(t, w)["id"].(string) + "/managed-context"
	commit := strings.Repeat("a", 40)
	public := map[string]string{
		"docs/AGENTS-KERNEL.md":      strings.Repeat("K", 6574),
		"docs/AGENTS-CORE.md":        strings.Repeat("C", 67056),
		"docs/AGENTS-VERSIONING.md":  strings.Repeat("V", 28000),
		"docs/AGENTS-DOMAIN-DEV.md":  strings.Repeat("D", 22000),
		"docs/AGENTS-DOMAIN-OPS.md":  strings.Repeat("O", 10000),
		"docs/AGENTS-AGENT-BUILD.md": "# Builder role\n\n- Only for builders.\n",
	}
	private := map[string]string{
		"docs/AGENTS-KERNEL-PRIVATE.md": strings.Repeat("P", 4149),
		"docs/AGENTS-CORE-PRIVATE.md":   strings.Repeat("I", 67056),
		"docs/AGENTS-PROFILE-OWNER.md":  "# Person profile\n\n- Owner-only instructions.\n",
		"docs/AGENTS-PROFILE-OTHER.md":  "# Person profile\n\n- Other person's instructions.\n",
	}
	grantDir := t.TempDir()
	grant, err := json.Marshal(map[string]any{"grants": []map[string]string{{"tenant_id": f.person.TenantID, "repository": "fixture/private"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(grantDir, "fixture-read.allowlist.json"), grant, 0600); err != nil {
		t.Fatal(err)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		for _, source := range []struct {
			repo, visibility, credential string
			paths                        []string
			files                        map[string]string
		}{
			{"fixture/company", "public", "", doctrine.DefaultPaths, public},
			{"fixture/private", "private", "fixture-read", []string{"docs/AGENTS-*.md", "docs/AGENTS-PROFILE-OWNER.md", "docs/AGENTS-PROFILE-OTHER.md"}, private},
		} {
			var id string
			if err := tx.QueryRow(t.Context(), `INSERT INTO doctrine_sources(tenant_id,repository,visibility,credential_ref,commit_sha,paths,indexed_at) VALUES($1,$2,$3,$4,$5,$6,clock_timestamp()) RETURNING id::text`, f.person.TenantID, source.repo, source.visibility, source.credential, commit, source.paths).Scan(&id); err != nil {
				return err
			}
			for name, body := range source.files {
				content := []byte(body)
				if _, err := tx.Exec(t.Context(), `INSERT INTO doctrine_cache(tenant_id,source_id,commit_sha,path,blob_sha,content) VALUES($1,$2,$3,$4,$5,$6)`, f.person.TenantID, id, commit, name, doctrine.BlobSHA(content), content); err != nil {
					return err
				}
			}
		}
		return nil
	})
	req := httptest.NewRequest("POST", endpoint, strings.NewReader(`{"pinned_files":true}`))
	req = req.WithContext(tenant.WithPrincipal(t.Context(), f.agent))
	req.Header.Set("Authorization", "Bearer "+f.key)
	req.Header.Set("X-Aeon-Worker-Lease", lease)
	w = httptest.NewRecorder()
	(doctrine.Credentials{Dir: grantDir}).CatalogMiddleware(f.mux).ServeHTTP(w, req)
	expect(t, w, 200)
	var got rules.Merged
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.PinnedFiles) != 2 {
		t.Fatalf("launch pack has %d files; want exactly the public and private kernels", len(got.PinnedFiles))
	}
	for i, want := range []struct{ repo, visibility, path, body string }{
		{"fixture/company", "public", "docs/AGENTS-KERNEL.md", public["docs/AGENTS-KERNEL.md"]},
		{"fixture/private", "private", "docs/AGENTS-KERNEL-PRIVATE.md", private["docs/AGENTS-KERNEL-PRIVATE.md"]},
	} {
		file := got.PinnedFiles[i]
		if file.Repository != want.repo || file.Visibility != want.visibility || file.Path != want.path || file.Content != want.body || file.Commit != commit || file.BlobSHA != doctrine.BlobSHA([]byte(want.body)) {
			t.Fatalf("launch pack entry %d does not match the pinned kernel", i)
		}
	}
	if err := rules.ValidateManagedFiles(got, got.Context, time.Now()); err != nil {
		t.Fatalf("kernel-only launch context is invalid: %v", err)
	}
}
