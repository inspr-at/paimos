// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func tierHistoryActions(t *testing.T, f *harnessFixture, path string) []string {
	t.Helper()
	w := f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	rows := decode(t, w)["history"].([]any)
	out := []string{}
	for _, item := range rows {
		h := item.(map[string]any)
		out = append(out, h["action"].(string))
		if h["actor_id"] == nil || h["actor_name"] == "" || h["at"] == nil {
			t.Fatal("history omitted attribution", h)
		}
	}
	return out
}
func TestTierHistoryPreservesEachTransitionAndUndo(t *testing.T) {
	f := fixture(t)
	path, lease, identity := tierSession(t, f)
	seedSyntheticTierPrice(t, f, path)
	ask := map[string]any{"request_id": uid(), "tier": "fast", "reason": "QA waits"}
	expect(t, f.call(f.agent, "POST", path+"/tier/ask", ask, ""), 201)
	decide := tierChangeBody(t, f, path, "fast", identity)
	decide["decision"] = "decline"
	expect(t, f.call(f.person, "POST", path+"/tier/requests/"+ask["request_id"].(string)+"/decision", decide, ""), 201)
	expect(t, f.call(f.person, "POST", path+"/tier/requests/"+ask["request_id"].(string)+"/decision", decide, ""), 201)
	ask["request_id"] = uid()
	expect(t, f.call(f.agent, "POST", path+"/tier/ask", ask, ""), 201)
	decide = tierChangeBody(t, f, path, "fast", identity)
	decide["decision"] = "approve"
	w := f.call(f.person, "POST", path+"/tier/requests/"+ask["request_id"].(string)+"/decision", decide, "")
	expect(t, w, 201)
	control := decode(t, w)["pending"].(map[string]any)["id"].(string)
	undo := tierChangeBody(t, f, path, "default", identity)
	undo["undo_of_control_id"] = control
	expect(t, f.call(f.person, "POST", path+"/tier", undo, ""), 201)
	expect(t, f.call(f.person, "POST", path+"/tier", undo, ""), 201)
	got := tierHistoryActions(t, f, path)
	want := []string{"cancelled", "approved", "requested", "declined", "requested"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cancel/replay lost history: %v", got)
	}
	// The same restored request is approved again, then confirmed and reversed.
	decide = tierChangeBody(t, f, path, "fast", identity)
	decide["decision"] = "approve"
	w = f.call(f.person, "POST", path+"/tier/requests/"+ask["request_id"].(string)+"/decision", decide, "")
	expect(t, w, 201)
	control = decode(t, w)["pending"].(map[string]any)["id"].(string)
	expect(t, f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease), 200)
	expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", map[string]any{"outcome": "applied", "reason": "tier_applied_safe_point"}, lease), 200)
	undo = tierChangeBody(t, f, path, "default", identity)
	undo["undo_of_control_id"] = control
	w = f.call(f.person, "POST", path+"/tier", undo, "")
	expect(t, w, 201)
	reverse := decode(t, w)["pending"].(map[string]any)["id"].(string)
	expect(t, f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease), 200)
	expect(t, f.call(f.agent, "POST", path+"/controls/"+reverse+"/complete", map[string]any{"outcome": "applied", "reason": "tier_applied_safe_point"}, lease), 200)
	got = tierHistoryActions(t, f, path)
	want = append([]string{"undone", "undo_requested", "changed", "approved"}, want...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("confirmed Undo history: %v", got)
	}
	w = f.call(f.person, "GET", path+"/tier", nil, "")
	changed := decode(t, w)["history"].([]any)[2].(map[string]any)
	if changed["actor_id"] != f.person.ID || changed["asked_by_name"] == nil || changed["from_tier"] != "default" || changed["to_tier"] != "fast" {
		t.Fatal("confirmation lost person/agent", changed)
	}
	expect(t, f.call(f.foreign, "GET", path+"/tier", nil, ""), 404)
	wrong := tierChangeBody(t, f, path, "fast", identity)
	wrong["undo_of_control_id"] = control
	expect(t, f.call(f.person, "POST", path+"/tier", wrong, ""), 409)
}
func TestTierEstimateHasSourceAndHonestEmptyTime(t *testing.T) {
	f := fixture(t)
	path, lease, _ := tierSession(t, f)
	seedSyntheticTierPrice(t, f, path)
	w := f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	if decode(t, w)["estimates"].([]any)[0].(map[string]any)["n"] != float64(0) {
		t.Fatal("invented sample")
	}
	expect(t, f.call(f.person, "POST", "/api/model-prices", map[string]any{"model": "fixture-model", "version": 1, "input_usd_per_million": "1", "output_usd_per_million": "1", "cached_input_usd_per_million": "1"}, ""), 201)
	in := map[string]any{"report_id": uid(), "model": "fixture-model", "sequence": 1, "service_tier": "default", "input_tokens": 2400000, "output_tokens": 0, "cached_input_tokens": 0, "provisional": false, "billing_mode": "api", "model_time_ms": 600000}
	expect(t, f.call(f.agent, "POST", path+"/usage", in, lease), 200)
	sessionID := path[strings.LastIndex(path, "/")+1:]
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed',started_at='2026-10-01 00:00:00+00',ended_at='2026-10-01 00:15:00+00' WHERE id=(SELECT run_id FROM harness_sessions WHERE id=$1)`, sessionID)
		return err
	})
	w = f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	state := decode(t, w)
	e := state["estimates"].([]any)[1].(map[string]any)
	if e["n"] != float64(1) || e["run_id"] == nil || e["cost_usd"] != "4.800000000000" || !strings.Contains(e["basis"].(string), "frozen price version 1") || e["duration_ms"] != nil {
		t.Fatal("source/unknown speed", e)
	}
	defaultEstimate := state["estimates"].([]any)[0].(map[string]any)
	if defaultEstimate["duration_ms"] != float64(900000) {
		t.Fatal("default changed tools/waits", defaultEstimate)
	}
	unpriced := state["estimates"].([]any)[2].(map[string]any)
	if unpriced["n"] != float64(0) || unpriced["cost_usd"] != nil {
		t.Fatal("invented unpriced estimate", unpriced)
	}
	// A later catalog revision does not reprice the sample's frozen base cost.
	expect(t, f.call(f.person, "POST", "/api/model-prices", map[string]any{"model": "fixture-model", "version": 2, "input_usd_per_million": "99", "output_usd_per_million": "99", "cached_input_usd_per_million": "99"}, ""), 201)
	w = f.call(f.person, "GET", path+"/tier", nil, "")
	expect(t, w, 200)
	if decode(t, w)["estimates"].([]any)[1].(map[string]any)["cost_usd"] != e["cost_usd"] {
		t.Fatal("catalog repriced history")
	}
	in["report_id"] = uid()
	in["sequence"] = 2
	in["model_time_ms"] = 599999
	expect(t, f.call(f.agent, "POST", path+"/usage", in, lease), 409)
}
