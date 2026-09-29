// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/jackc/pgx/v5"
)

func TestManagedSettingsCatalogLifecycleAndFencing(t *testing.T) {
	f := fixture(t)
	order, run := stateRun(t, f, f.project, "running", "SET-10")
	lease := "managed-settings-lease-00000000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "fixture", "harness_session_ref": "managed-settings-reference-000000001", "worker_lease": lease, "management_mode": "managed", "role": "worker", "run_id": run, "work_order_id": order, "ticket_node_id": order, "work_shape": "ship", "model": "fixture-model", "reasoning_effort": "high", "advertised_capabilities": []string{"managed_control_v1", "stop", "rename", "model", "effort"}}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	identity := ownedprocess.Identity{DaemonID: "fixture", Generation: strings.Repeat("a", 32), ProcessID: strings.Repeat("b", 32), RootPID: 1234, GroupID: 1234, StartedAt: time.Now().UTC().Truncate(time.Second)}
	account := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label) VALUES($1,$2,'settings-fixture','claude','fixture',$3,'Settings account')`, f.person.TenantID, account, f.agent.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET daemon_id=$2,daemon_generation=$3,account_id=$4 WHERE id=$1`, run, identity.DaemonID, identity.Generation, account); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'settings-high','1','claude','anthropic','fixture-model','high','standard'),($1,'settings-low','1','claude','anthropic','fixture-model','low','standard'),($1,'settings-other','1','claude','anthropic','other-model','high','standard')`, f.person.TenantID)
		return err
	})
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "process_ownership": identity}, lease), 200)
	w = f.call(f.person, "GET", path+"/managed-settings", nil, "")
	expect(t, w, 200)
	if len(decode(t, w)["models"].([]any)) != 2 {
		t.Fatal(w.Body.String())
	}
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "viewer")
	expect(t, f.call(f.person, "GET", path+"/managed-settings", nil, ""), 403)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	body := map[string]any{"request_id": uid(), "kind": "model", "value": "outside-catalog", "expected_ownership": identity}
	expect(t, f.call(f.person, "POST", path+"/managed-controls", body, ""), 400)
	body["value"] = "other-model"
	expect(t, f.call(f.agent, "POST", path+"/managed-controls", body, lease), 403)
	wrong := identity
	wrong.Generation = strings.Repeat("c", 32)
	body["expected_ownership"] = wrong
	expect(t, f.call(f.person, "POST", path+"/managed-controls", body, ""), 409)
	body["expected_ownership"] = identity
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET harness='codex' WHERE id=$1`, id)
		return err
	})
	expect(t, f.call(f.person, "POST", path+"/managed-controls", body, ""), 409)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET harness='claude' WHERE id=$1`, id)
		return err
	})
	for _, tc := range []struct{ kind, value, outcome string }{{"model", "other-model", "rejected"}, {"effort", "low", "applied"}, {"rename", "Focused session", "applied"}} {
		body["request_id"], body["kind"], body["value"] = uid(), tc.kind, tc.value
		w = f.call(f.person, "POST", path+"/managed-controls", body, "")
		expect(t, w, 201)
		control := decode(t, w)["id"].(string)
		expect(t, f.call(f.person, "POST", path+"/managed-controls", body, ""), 201)
		body["value"] = "changed"
		expect(t, f.call(f.person, "POST", path+"/managed-controls", body, ""), map[bool]int{true: 400, false: 409}[tc.kind == "effort"])
		w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
		expect(t, w, 200)
		offered := decode(t, w)["controls"].([]any)
		if len(offered) != 1 || offered[0].(map[string]any)["value"] != tc.value {
			t.Fatal(w.Body.String())
		}
		completion := map[string]any{"outcome": tc.outcome, "reason": "setting_" + tc.outcome}
		expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", completion, lease), 200)
		expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", completion, lease), 200)
	}
	// A setting may expire, but it must never change the public label later.
	body["request_id"], body["kind"], body["value"] = uid(), "rename", "Never applied"
	w = f.call(f.person, "POST", path+"/managed-controls", body, "")
	expect(t, w, 201)
	expired := decode(t, w)["id"].(string)
	body["request_id"] = uid()
	expect(t, f.call(f.person, "POST", path+"/managed-controls", body, ""), 409)
	expect(t, f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease), 200)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_controls SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, expired)
		return err
	})
	expect(t, f.call(f.agent, "POST", path+"/controls/"+expired+"/complete", map[string]any{"outcome": "applied", "reason": "setting_applied"}, lease), 409)
	w = f.call(f.person, "GET", path+"/controls/"+expired, nil, "")
	expect(t, w, 200)
	if decode(t, w)["reason"] != "outcome_unconfirmed" {
		t.Fatal(w.Body.String())
	}
	// Revoking account grants after enqueue is checked again at claim.
	body["request_id"], body["kind"], body["value"] = uid(), "model", "other-model"
	w = f.call(f.person, "POST", path+"/managed-controls", body, "")
	expect(t, w, 201)
	revoked := decode(t, w)["id"].(string)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var label string
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT display_label FROM harness_sessions WHERE id=$1`, id).Scan(&label); err != nil {
			return err
		}
		if label != "Focused session" {
			t.Fatal(label)
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_metadata_changes WHERE session_id=$1 AND field='display_label'`, id).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("rename history %d", n)
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET allowed_model_profile_ids='{}' WHERE id=$1`, account)
		return err
	})
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	if len(decode(t, w)["controls"].([]any)) != 0 {
		t.Fatal("revoked catalog setting delivered")
	}
	w = f.call(f.person, "GET", path+"/controls/"+revoked, nil, "")
	expect(t, w, 200)
	if decode(t, w)["reason"] != "setting_catalog_changed" {
		t.Fatal(w.Body.String())
	}
	body["request_id"], body["kind"], body["value"] = uid(), "model", "other-model"
	expect(t, f.call(f.person, "POST", path+"/managed-controls", body, ""), 400)
}
