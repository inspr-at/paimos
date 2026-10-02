// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/servicetier"
	"github.com/inspr-at/paimos/internal/tenant"
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

// Orchestration tests use a synthetic stored price to exercise confirmation,
// Undo and frozen accounting independently of vendor pricing availability.
// This fixture is never submitted as a vendor report; report ingestion must
// reject it. Its artificial numbers are not catalog entries or vendor prices.
func seedSyntheticTierPrice(t *testing.T, f *harnessFixture, path string) {
	t.Helper()
	r := servicetier.Advertised("codex", "fixture-model", "0.159.2")
	price, usage := 2.0, 2.5
	r.Source = "synthetic orchestration test fixture (not vendor pricing)"
	r.Tiers[1] = servicetier.Tier{Tier: "fast", Name: "Fast", Offered: true, PriceMultiplier: &price, UsageMultiplier: &usage, Mechanism: "service_tier=fast"}
	raw, err := json.Marshal([]servicetier.Report{r})
	if err != nil {
		t.Fatal(err)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET service_tier_reports=$2 WHERE id=$1`, path[strings.LastIndex(path, "/")+1:], raw)
		return err
	})
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
	seedSyntheticTierPrice(t, f, path)
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
	seedSyntheticTierPrice(t, f, path)
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
	seedSyntheticTierPrice(t, f, path)
	expect(t, f.call(f.foreign, "GET", path+"/tier", nil, ""), 404)
	otherProject := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'TIER-11',kind_id,'Other project' FROM nodes WHERE id=$3`, f.person.TenantID, otherProject, f.project)
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

func TestTierUsagePersistsFrozenCostAcrossChanges(t *testing.T) {
	f := fixture(t)
	path, lease, identity := tierSession(t, f)
	seedSyntheticTierPrice(t, f, path)
	expect(t, f.call(f.person, "POST", "/api/model-prices", map[string]any{"model": "fixture-model", "version": 1, "input_usd_per_million": "1", "output_usd_per_million": "1", "cached_input_usd_per_million": "1"}, ""), 201)
	for i, tc := range []struct{ tier, cost string }{{"default", "0.000100000000"}, {"fast", "0.000300000000"}, {"default", "0.000400000000"}} {
		if i > 0 {
			w := f.call(f.person, "POST", path+"/tier", tierChangeBody(t, f, path, tc.tier, identity), "")
			expect(t, w, 201)
			id := decode(t, w)["pending"].(map[string]any)["id"].(string)
			expect(t, f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease), 200)
			expect(t, f.call(f.agent, "POST", path+"/controls/"+id+"/complete", map[string]any{"outcome": "applied", "reason": "tier_applied_safe_point"}, lease), 200)
		}
		payload := usagePayload()
		payload["model"], payload["sequence"], payload["input_tokens"] = "fixture-model", i+1, 100*(i+1)
		payload["output_tokens"], payload["cached_input_tokens"], payload["billing_mode"] = 0, 0, "api"
		// A session reporter may omit the field: the confirmed active tier is used.
		out, _ := usageResult(t, f.call(f.agent, "POST", path+"/usage", payload, lease))
		if out.ServiceTier == nil || *out.ServiceTier != tc.tier || out.EstimatedCostUSD == nil || *out.EstimatedCostUSD != tc.cost {
			t.Fatalf("tier/cost not persisted: %+v", out)
		}
		if i > 0 && len(out.TierSegments) != 2 {
			t.Fatal("usage did not retain both frozen multipliers")
		}
	}
}

func TestTierLastChangeExposesRejectedOutcome(t *testing.T) {
	f := fixture(t)
	path, lease, identity := tierSession(t, f)
	seedSyntheticTierPrice(t, f, path)
	w := f.call(f.person, "POST", path+"/tier", tierChangeBody(t, f, path, "fast", identity), "")
	expect(t, w, 201)
	control := decode(t, w)["pending"].(map[string]any)["id"].(string)
	expect(t, f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease), 200)
	expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", map[string]any{"outcome": "rejected", "reason": "vendor_rejected"}, lease), 200)
	w = f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	out := decode(t, w)
	last, ok := out["last_change"].(map[string]any)
	if !ok || last["id"] != control || last["state"] != "completed" || last["outcome"] != "rejected" || last["reason"] != "vendor_rejected" || out["active_tier"] != "default" || out["pending"] != nil {
		t.Fatalf("missing honest rejected outcome: %s", w.Body.String())
	}
	expect(t, f.call(f.foreign, "GET", path+"/tier", nil, ""), 404)
}

func TestTierRequestVisibleOnUnopenedListRow(t *testing.T) {
	f := fixture(t)
	path, _, identity := tierSession(t, f)
	seedSyntheticTierPrice(t, f, path)
	w := f.call(f.agent, "POST", path+"/tier/ask", map[string]any{"request_id": uid(), "tier": "fast", "reason": "QA waits"}, "")
	expect(t, w, 201)
	request := decode(t, w)["id"].(string)
	id := path[strings.LastIndex(path, "/")+1:]
	check := func(want any) {
		t.Helper()
		w := f.call(f.person, "GET", "/api/harness-sessions?project="+f.project, nil, "")
		expect(t, w, 200)
		for _, raw := range decode(t, w)["items"].([]any) {
			row := raw.(map[string]any)
			if row["id"] == id {
				value, present := row["service_tier_request"]
				// With no pending request the optional field must be omitted,
				// preserving the frozen no-request status/heartbeat bytes.
				if present != (want != nil) || value != want {
					t.Fatalf("unopened list request = %v (present %v), want %v", value, present, want)
				}
				return
			}
		}
		t.Fatal("requested session missing from list")
	}
	check("fast")
	body := tierChangeBody(t, f, path, "fast", identity)
	body["decision"] = "decline"
	expect(t, f.call(f.person, "POST", path+"/tier/requests/"+request+"/decision", body, ""), 201)
	check(nil)
}

func TestTierReportRejectsUnpinnedFactorsAtomically(t *testing.T) {
	f := fixture(t)
	path, lease, identity := tierSession(t, f)
	before := decode(t, f.call(f.person, "GET", path+"/tier", nil, ""))
	for _, tc := range []struct {
		name string
		edit func(*servicetier.Report)
	}{
		{"fast-price", func(r *servicetier.Report) {
			n := 6.0
			r.Tiers[1].Offered, r.Tiers[1].PriceMultiplier, r.Tiers[1].Mechanism = true, &n, "service_tier=fast"
		}},
		{"fast-speed", func(r *servicetier.Report) { n := 2.5; r.Tiers[1].SpeedFactor = &n }},
		{"fast-usage", func(r *servicetier.Report) { n := 2.5; r.Tiers[1].UsageMultiplier = &n }},
		{"fastest-price", func(r *servicetier.Report) {
			n := 8.0
			r.Tiers[2].Offered, r.Tiers[2].PriceMultiplier, r.Tiers[2].Mechanism = true, &n, "service_tier=ultrafast"
		}},
		{"default-speed", func(r *servicetier.Report) { n := 3.0; r.Tiers[0].SpeedFactor = &n }},
		{"default-usage", func(r *servicetier.Report) { n := 3.0; r.Tiers[0].UsageMultiplier = &n }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := servicetier.Advertised("codex", "fixture-model", "0.159.2")
			tc.edit(&r)
			// A valid report first proves the whole request rolls back if a later
			// model tries to inject a price or multiplier.
			good := servicetier.Advertised("codex", "gpt-6.1-sol", "0.159.2")
			expect(t, f.call(f.agent, "POST", path+"/tier/report", map[string]any{"reports": []servicetier.Report{good, r}}, lease), 400)
			w := f.call(f.person, "GET", path+"/tier", nil, "")
			expect(t, w, 200)
			after := decode(t, w)
			if after["revision"] != before["revision"] || after["active_tier"] != "default" {
				t.Fatal("rejected report changed tier state")
			}
			reports, _ := json.Marshal(after["reports"])
			original, _ := json.Marshal(before["reports"])
			if string(reports) != string(original) {
				t.Fatal("rejected report changed persisted catalog")
			}
		})
	}
	for _, tier := range []string{"fast", "fastest"} {
		expect(t, f.call(f.person, "POST", path+"/tier", tierChangeBody(t, f, path, tier, identity), ""), 400)
		expect(t, f.call(f.agent, "POST", path+"/tier/ask", map[string]any{"request_id": uid(), "tier": tier, "reason": "must not invent a price"}, ""), 400)
	}
}

func TestTierAlreadyActiveRetainsIdempotencyReceipt(t *testing.T) {
	f := fixture(t)
	path, lease, identity := tierSession(t, f)
	// Keep the control workflow fixture price independent of report validation.
	// A changed tier would otherwise be accepted without a stored receipt.
	seedSyntheticTierPrice(t, f, path)
	body := tierChangeBody(t, f, path, "default", identity)
	w := f.call(f.person, "POST", path+"/tier", body, "")
	expect(t, w, 201)
	state := decode(t, w)
	if state["active_tier"] != "default" || state["pending"] != nil || state["revision"] != body["expected_revision"] {
		t.Fatal("already-active request queued work or changed tier revision")
	}
	expect(t, f.call(f.person, "POST", path+"/tier", body, ""), 201)
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"tier", func(b map[string]any) { b["tier"] = "fast" }},
		{"revision", func(b map[string]any) { b["expected_revision"] = state["revision"].(float64) + 1 }},
		{"ownership", func(b map[string]any) {
			wrong := identity
			wrong.Generation = strings.Repeat("c", 32)
			b["expected_ownership"] = wrong
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := map[string]any{}
			for k, v := range body {
				changed[k] = v
			}
			tc.edit(changed)
			expect(t, f.call(f.person, "POST", path+"/tier", changed, ""), 409)
		})
	}
	other := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, other, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','other controller')`, other.TenantID, other.ID)
		return err
	})
	dbtest.BindRole(t, f.db, other.TenantID, other.ID, "admin")
	expect(t, f.call(other, "POST", path+"/tier", body, ""), 409)
	// Refreshing the report changes the revision. The original retry must still
	// succeed because its completed receipt precedes revision/ownership checks.
	expect(t, f.call(f.agent, "POST", path+"/tier/report", map[string]any{"reports": servicetier.Reports("codex", "fixture-model", "0.159.2")}, lease), 200)
	expect(t, f.call(f.person, "POST", path+"/tier", body, ""), 201)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_controls WHERE session_id=$1`, path[strings.LastIndex(path, "/")+1:]).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("already-active retries created %d controls, want one receipt", count)
		}
		var completed bool
		err := tx.QueryRow(t.Context(), `SELECT state='completed' AND outcome='applied' AND reason='tier_already_active' AND request_digest IS NOT NULL AND completed_at IS NOT NULL FROM harness_controls WHERE id=$1`, body["request_id"]).Scan(&completed)
		if err == nil && !completed {
			t.Fatal("already-active receipt was not durably completed")
		}
		return err
	})
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	if len(decode(t, w)["controls"].([]any)) != 0 {
		t.Fatal("completed no-op receipt was offered to daemon")
	}
}

func TestUnmanagedCodexTierInstructions(t *testing.T) {
	f := fixture(t)
	path, _, _ := usageSession(t, f, "unmanaged")
	w := f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	state := decode(t, w)
	reason, _ := state["read_only_reason"].(string)
	if state["read_only"] != true || !strings.Contains(reason, "service_tier") || strings.Contains(reason, "/fast") {
		t.Fatalf("incorrect read-only Codex instructions: %q", reason)
	}
}
