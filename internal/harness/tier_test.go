// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/servicetier"
	"github.com/jackc/pgx/v5"
)

func tierSession(t *testing.T, f *harnessFixture) (string, string, ownedprocess.Identity) {
	t.Helper()
	order, run := stateRun(t, f, f.project, "running", "TIER-10")
	lease := "tier-fixture-lease-0000000000000000001"
	identity := ownedprocess.Identity{DaemonID: "fixture", Generation: strings.Repeat("a", 32), ProcessID: strings.Repeat("b", 32), RootPID: 1234, GroupID: 1234, StartedAt: time.Now().UTC().Truncate(time.Second)}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'tier-fixture','1','codex','openai','fixture-model','high','standard')`, f.person.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET daemon_id=$2,daemon_generation=$3 WHERE id=$1`, run, identity.DaemonID, identity.Generation)
		return err
	})
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "fixture", "harness_session_ref": uid(), "worker_lease": lease, "management_mode": "managed", "role": "worker", "run_id": run, "work_order_id": order, "ticket_node_id": order, "work_shape": "ship", "model": "fixture-model", "reasoning_effort": "high", "advertised_capabilities": []string{"stop", servicetier.Capability}}, "")
	expect(t, w, 201)
	path := base + "/" + decode(t, w)["id"].(string)
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "idle", "activity_sequence": 1, "process_ownership": identity}, lease), 200)
	r := servicetier.Advertised("codex", "gpt-6.1-sol", "0.159.2")
	r.Model = "fixture-model"
	expect(t, f.call(f.agent, "POST", path+"/tier/report", map[string]any{"reports": []servicetier.Report{r}, "active_tier": "default"}, lease), 200)
	return path, lease, identity
}
func tierChangeBody(t *testing.T, f *harnessFixture, path, tier string, identity ownedprocess.Identity) map[string]any {
	t.Helper()
	w := f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	return map[string]any{"request_id": uid(), "tier": tier, "expected_revision": decode(t, w)["revision"], "expected_ownership": identity}
}
func TestTierChangeConfirmUndoAndSafePoint(t *testing.T) {
	f := fixture(t)
	path, lease, identity := tierSession(t, f)
	body := tierChangeBody(t, f, path, "fast", identity)
	w := f.call(f.person, "POST", path+"/tier", body, "")
	expect(t, w, 201)
	state := decode(t, w)
	if state["active_tier"] != "default" || state["pending"] == nil {
		t.Fatal("change claimed active before confirmation")
	}
	control := state["pending"].(map[string]any)["id"].(string)
	expect(t, f.call(f.person, "POST", path+"/tier", body, ""), 201)
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 2, "process_ownership": identity}, lease), 200)
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	if len(decode(t, w)["controls"].([]any)) != 0 {
		t.Fatal("tier offered during an active turn")
	}
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "idle", "activity_sequence": 3, "process_ownership": identity}, lease), 200)
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	if len(decode(t, w)["controls"].([]any)) != 1 {
		t.Fatal(w.Body.String())
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var bounded bool
		err := tx.QueryRow(t.Context(), `SELECT expires_at-claimed_at<=interval '45 seconds' FROM harness_controls WHERE id=$1`, control).Scan(&bounded)
		if err == nil && !bounded {
			t.Fatal("claimed tier authorization retained the pending wait window")
		}
		return err
	})
	expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", map[string]any{"outcome": "applied", "reason": "tier_applied_safe_point"}, lease), 200)
	w = f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	if decode(t, w)["active_tier"] != "fast" || decode(t, w)["pending"] != nil {
		t.Fatal(w.Body.String())
	}
	undo := tierChangeBody(t, f, path, "default", identity)
	w = f.call(f.person, "POST", path+"/tier", undo, "")
	expect(t, w, 201)
	if decode(t, w)["active_tier"] != "fast" {
		t.Fatal("undo changed active without confirmation")
	}
	control = decode(t, w)["pending"].(map[string]any)["id"].(string)
	expect(t, f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease), 200)
	expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", map[string]any{"outcome": "applied", "reason": "tier_applied_safe_point"}, lease), 200)
	w = f.call(f.person, "GET", path+"/tier", nil, "")
	if decode(t, w)["active_tier"] != "default" {
		t.Fatal(w.Body.String())
	}
}
func TestTierAskApproveDeclineCancelAndAuthorization(t *testing.T) {
	f := fixture(t)
	path, lease, identity := tierSession(t, f)
	ask := map[string]any{"request_id": uid(), "tier": "fast", "reason": "QA waits for this turn"}
	expect(t, f.call(f.person, "POST", path+"/tier/ask", ask, ""), 403)
	w := f.call(f.agent, "POST", path+"/tier/ask", ask, "")
	expect(t, w, 201)
	request := decode(t, w)["id"].(string)
	expect(t, f.call(f.agent, "POST", path+"/tier/ask", ask, ""), 201)
	body := tierChangeBody(t, f, path, "fast", identity)
	expect(t, f.call(f.agent, "POST", path+"/tier", body, lease), 403)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "viewer")
	expect(t, f.call(f.person, "POST", path+"/tier", body, ""), 403)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	body["decision"] = "approve"
	w = f.call(f.person, "POST", path+"/tier/requests/"+request+"/decision", body, "")
	expect(t, w, 201)
	if decode(t, w)["requests"].([]any)[0].(map[string]any)["state"] != "approved" {
		t.Fatal(w.Body.String())
	}
	expect(t, f.call(f.person, "POST", path+"/tier/requests/"+request+"/decision", body, ""), 201)
	undo := tierChangeBody(t, f, path, "default", identity)
	w = f.call(f.person, "POST", path+"/tier", undo, "")
	expect(t, w, 201)
	expect(t, f.call(f.person, "POST", path+"/tier", undo, ""), 201)
	if decode(t, w)["pending"] != nil || decode(t, w)["requests"].([]any)[0].(map[string]any)["state"] != "pending" {
		t.Fatal("cancel did not restore the request")
	}
	body = tierChangeBody(t, f, path, "fast", identity)
	body["decision"] = "decline"
	w = f.call(f.person, "POST", path+"/tier/requests/"+request+"/decision", body, "")
	expect(t, w, 201)
	if decode(t, w)["requests"].([]any)[0].(map[string]any)["state"] != "declined" || decode(t, w)["active_tier"] != "default" {
		t.Fatal(w.Body.String())
	}
	unsupported := tierChangeBody(t, f, path, "fastest", identity)
	expect(t, f.call(f.person, "POST", path+"/tier", unsupported, ""), 400)
	expect(t, f.call(f.agent, "POST", path+"/tier/report", map[string]any{"reports": []servicetier.Report{servicetier.Advertised("codex", "fixture-model", "0.159.2")}, "active_tier": "fast"}, lease), 409)
}
func TestTierUnmanagedEndedAndOwnershipFence(t *testing.T) {
	f := fixture(t)
	path, lease, identity := tierSession(t, f)
	body := tierChangeBody(t, f, path, "fast", identity)
	wrong := identity
	wrong.Generation = strings.Repeat("c", 32)
	body["expected_ownership"] = wrong
	expect(t, f.call(f.person, "POST", path+"/tier", body, ""), 409)
	body["expected_ownership"] = identity
	_, id, _ := usageSession(t, f, "unmanaged")
	unmanaged := "/api/projects/" + f.project + "/harness-sessions/" + id
	expect(t, f.call(f.person, "POST", unmanaged+"/tier", body, ""), 409)
	expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "process_exited"}, lease), 200)
	expect(t, f.call(f.person, "POST", path+"/tier", body, ""), 409)
	w := f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	if decode(t, w)["read_only"] != true {
		t.Fatal(w.Body.String())
	}
}

func TestTierIsolationRevocationAndReportFreeze(t *testing.T) {
	f := fixture(t)
	path, lease, identity := tierSession(t, f)
	expect(t, f.call(f.foreign, "GET", path+"/tier", nil, ""), 404)
	otherProject := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'TIER-OTHER',kind_id,'Other project' FROM nodes WHERE id=$3`, f.person.TenantID, otherProject, f.project)
		return err
	})
	expect(t, f.call(f.person, "GET", strings.Replace(path, f.project, otherProject, 1)+"/tier", nil, ""), 404)
	report := map[string]any{"reports": servicetier.Reports("codex", "fixture-model", "0.159.2")}
	expect(t, f.call(f.person, "POST", path+"/tier/report", report, lease), 403)
	expect(t, f.call(f.agent, "POST", path+"/tier/report", report, "wrong-lease-00000000000000000000000"), 403)
	ask := map[string]any{"request_id": uid(), "tier": "fast", "reason": "QA"}
	expect(t, f.call(f.agent, "POST", path+"/tier/ask", ask, ""), 201)
	f.tx(t, f.foreign, func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_tier_requests`).Scan(&count)
		if count != 0 {
			t.Fatal("requests escaped tenant RLS")
		}
		return err
	})
	body := tierChangeBody(t, f, path, "fast", identity)
	expect(t, f.call(f.person, "POST", path+"/managed-controls", map[string]any{"request_id": uid(), "kind": "tier", "value": "fast", "expected_ownership": identity}, ""), 400)
	w := f.call(f.person, "POST", path+"/tier", body, "")
	expect(t, w, 201)
	control := decode(t, w)["pending"].(map[string]any)["id"].(string)
	expect(t, f.call(f.agent, "POST", path+"/tier/report", report, lease), 409)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "viewer")
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	if len(decode(t, w)["controls"].([]any)) != 0 {
		t.Fatal("revoked person control was claimed by daemon")
	}
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	w = f.call(f.person, "GET", path+"/controls/"+control, nil, "")
	expect(t, w, 200)
	if decode(t, w)["reason"] != "authorization_revoked" {
		t.Fatal(w.Body.String())
	}
	w = f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	if decode(t, w)["active_tier"] != "default" {
		t.Fatal("rejection changed active tier")
	}
}
