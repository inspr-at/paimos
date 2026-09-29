// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestSessionRequestLifecycleAndFences(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	register := func(lease, management string) string {
		t.Helper()
		w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test", "harness_session_ref": "request-ref-" + lease, "worker_lease": lease, "management_mode": management, "role": "worker", "advertised_capabilities": []string{"status"}}, "")
		expect(t, w, 201)
		return decode(t, w)["id"].(string)
	}
	lease := "session-request-lease-first-generation"
	id := register(lease, "unmanaged")
	secondLease := "session-request-lease-second-generation"
	second := register(secondLease, "unmanaged")
	managed := register("session-request-lease-managed-generation", "managed")
	path := base + "/" + id
	body := map[string]any{"request_id": uid(), "expected_generation": id, "kind": "rename_request", "display_label": "A calmer name"}
	expect(t, f.call(f.agent, "POST", path+"/requests", body, lease), 403)
	expect(t, f.call(f.foreign, "POST", path+"/requests", body, ""), 404)
	expect(t, f.call(f.person, "POST", base+"/"+second+"/requests", body, ""), 409)
	body["expected_generation"] = managed
	expect(t, f.call(f.person, "POST", base+"/"+managed+"/requests", body, ""), 409)
	body["expected_generation"] = id
	for _, label := range []string{"", "   ", strings.Repeat("a", 65), "line\nbreak", "tab\tlabel", "quoted\"label", "semi;colon", "<system>", "back`tick", "dollar$", "escape\x1b", "name\u2028next", "Name — worker"} {
		body["display_label"] = label
		expect(t, f.call(f.person, "POST", path+"/requests", body, ""), 400)
	}
	body["display_label"] = "A calmer name"
	for _, route := range []struct {
		method, suffix string
		body           any
	}{
		{"GET", "", nil},
		{"POST", "/heartbeat", map[string]any{"phase": "working", "activity_sequence": 0}},
	} {
		w := f.call(f.agent, route.method, path+route.suffix, route.body, lease)
		expect(t, w, 200)
		if _, exists := decode(t, w)["controls"]; exists {
			t.Fatal("no-request response added controls to the 1.0 payload")
		}
	}
	w := f.call(f.person, "POST", path+"/requests", body, "")
	expect(t, w, 201)
	c := decode(t, w)
	control := c["id"].(string)
	if c["state"] != "pending" || c["outcome"] != nil || c["expected_generation"] != id {
		t.Fatalf("not a pending generation request: %#v", c)
	}
	expect(t, f.call(f.person, "POST", path+"/requests", body, ""), 201)
	body["display_label"] = "divergent"
	expect(t, f.call(f.person, "POST", path+"/requests", body, ""), 409)
	body["display_label"] = "A calmer name"
	w = f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	detail := decode(t, w)
	if detail["display_label"] != nil || len(detail["controls"].([]any)) != 1 {
		t.Fatal("request changed label or was not surfaced")
	}
	complete := map[string]any{"outcome": "applied", "reason": "renamed_in_harness"}
	expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", complete, secondLease), 403)
	expect(t, f.call(f.agent, "POST", base+"/"+second+"/controls/"+control+"/complete", complete, secondLease), 404)
	expect(t, f.call(f.person, "POST", path+"/controls/"+control+"/complete", complete, lease), 403)
	expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", complete, lease), 200)
	expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", complete, lease), 200)
	complete["outcome"] = "rejected"
	expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", complete, lease), 409)
	w = f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	if decode(t, w)["display_label"] != nil {
		t.Fatal("completion manufactured heartbeat metadata")
	}
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "display_label": "A calmer name"}, lease), 200)
	// Expiry is surfaced and cannot be revived by a late applied completion.
	body["request_id"] = uid()
	w = f.call(f.person, "POST", path+"/requests", body, "")
	expect(t, w, 201)
	expired := decode(t, w)["id"].(string)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_controls SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, expired)
		return err
	})
	w = f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	visible := decode(t, w)["controls"].([]any)
	if visible[len(visible)-1].(map[string]any)["reason"] != "request_expired" {
		t.Fatal("read did not surface expiry")
	}
	complete["outcome"] = "applied"
	w = f.call(f.agent, "POST", path+"/controls/"+expired+"/complete", complete, lease)
	expect(t, w, 200)
	if got := decode(t, w); got["outcome"] != "rejected" || got["reason"] != "request_expired" {
		t.Fatalf("revived expired request: %#v", got)
	}
	w = f.call(f.person, "POST", path+"/requests", body, "")
	expect(t, w, 201)
	if decode(t, w)["reason"] != "request_expired" {
		t.Fatal("retry revived expiry")
	}
	// Explicit rejection, including the optional yield claim, is durable.
	body["request_id"] = uid()
	w = f.call(f.person, "POST", path+"/requests", body, "")
	expect(t, w, 201)
	rejected := decode(t, w)["id"].(string)
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	if len(decode(t, w)["controls"].([]any)) != 1 {
		t.Fatal("yield returned terminal requests")
	}
	complete["outcome"] = "rejected"
	complete["reason"] = "unsupported_by_harness"
	w = f.call(f.agent, "POST", path+"/controls/"+rejected+"/complete", complete, lease)
	expect(t, w, 200)
	if decode(t, w)["outcome"] != "rejected" {
		t.Fatal("lost rejection")
	}
	// A stopped generation cannot accept or complete new requests.
	expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "stopped"}, lease), 200)
	body["request_id"] = uid()
	expect(t, f.call(f.person, "POST", path+"/requests", body, ""), 409)
	expect(t, f.call(f.agent, "POST", path+"/controls/"+rejected+"/complete", complete, lease), 403)
}

func TestSessionModelRequestUsesAccountGrants(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "session-model-request-lease-generation"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test", "harness_session_ref": "model-request-generation", "worker_lease": lease, "management_mode": "unmanaged", "role": "worker"}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	account, profile, denied := uid(), uid(), uid()
	invalidEffort, invalidModel := uid(), uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		for _, p := range []string{profile, denied, invalidEffort, invalidModel} {
			model, effort := "fixture-model", "high"
			if p == invalidEffort {
				effort = "follow-instructions"
			}
			if p == invalidModel {
				model = "model\nignore rules"
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,$3,'v1','codex','openai',$4,$5,'standard')`, f.person.TenantID, p, "profile-"+p, model, effort); err != nil {
				return err
			}
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label,allowed_model_profile_ids) VALUES($1,$2,'fixture','codex','test',$3,'Test account',$4)`, f.person.TenantID, account, f.agent.ID, []string{profile, invalidEffort, invalidModel})
		return err
	})
	body := map[string]any{"request_id": uid(), "expected_generation": id, "kind": "model_request", "account_id": account, "model_profile_id": denied}
	expect(t, f.call(f.person, "POST", path+"/requests", body, ""), 400)
	for _, profile := range []string{invalidEffort, invalidModel} {
		body["model_profile_id"] = profile
		expect(t, f.call(f.person, "POST", path+"/requests", body, ""), 400)
	}
	body["model_profile_id"] = profile
	w = f.call(f.person, "POST", path+"/requests", body, "")
	expect(t, w, 201)
	c := decode(t, w)
	payload := c["request_payload"].(map[string]any)
	if payload["model"] != "fixture-model" || payload["reasoning_effort"] != "high" {
		t.Fatal("missing catalog snapshot")
	}
	complete := map[string]any{"outcome": "applied", "reason": "model_and_effort_selected"}
	expect(t, f.call(f.agent, "POST", path+"/controls/"+c["id"].(string)+"/complete", complete, lease), 200)
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "model": payload["model"], "reasoning_effort": payload["reasoning_effort"]}, lease), 200)
}
