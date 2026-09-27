// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestSessionMetadataValidationStorageAndLiveIsolation(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "metadata-lease-000000000000000000000001"
	registration := map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "build-host",
		"harness_session_ref": "metadata-ref-000000000000000001", "worker_lease": lease,
		"management_mode": "unmanaged", "role": "worker",
		"model": " gpt-6-sol ", "reasoning_effort": " xhigh ",
		"account_label": " Codex\n Pro ", "harness_version": " 1.2.3 ",
		"brief": " AEON-213 ", "worktree": " /Code/aeon ", "branch": " tm1.session-metadata ",
	}
	w := f.call(f.person, "POST", base, registration, "")
	expect(t, w, 201)
	s := decode(t, w)
	id := s["id"].(string)
	for key, want := range map[string]string{
		"model": "gpt-6-sol", "reasoning_effort": "xhigh", "account_label": "Codex Pro",
		"harness_version": "1.2.3", "brief": "AEON-213", "worktree": "/Code/aeon", "branch": "tm1.session-metadata",
	} {
		if s[key] != want {
			t.Fatalf("registered %s = %v, want %s", key, s[key], want)
		}
	}
	if len(s["commits"].([]any)) != 0 {
		t.Fatal("new session has commits")
	}
	expect(t, f.call(f.person, "POST", base, registration, ""), 201)
	registration["model"] = "other-model"
	expect(t, f.call(f.person, "POST", base, registration, ""), 409)
	registration["harness_session_ref"] = "metadata-ref-000000000000000002"
	registration["account_label"] = strings.Repeat("界", 129)
	expect(t, f.call(f.person, "POST", base, registration, ""), 400)

	path := base + "/" + id
	commits := make([]map[string]string, 20)
	for i := range commits {
		commits[i] = map[string]string{"sha": fmt.Sprintf("%07x", i+1), "subject": fmt.Sprintf(" Change %d ", i+1)}
	}
	heartbeat := map[string]any{
		"phase": "working", "activity": "busy", "activity_sequence": 1,
		"account_label": " Codex\tPro ", "brief": " AEON-214 ", "commits": commits,
	}
	w = f.call(f.agent, "POST", path+"/heartbeat", heartbeat, lease)
	expect(t, w, 200)
	s = decode(t, w)
	if s["account_label"] != "CodexPro" || s["brief"] != "AEON-214" || len(s["commits"].([]any)) != 20 {
		t.Fatalf("heartbeat metadata was not stored: %s", w.Body.String())
	}
	heartbeat["commits"] = []map[string]string{{"sha": "0000014", "subject": "Last change"}, {"sha": "0000015", "subject": "New change"}}
	w = f.call(f.agent, "POST", path+"/heartbeat", heartbeat, lease)
	expect(t, w, 200)
	items := decode(t, w)["commits"].([]any)
	if len(items) != 20 || items[0].(map[string]any)["sha"] != "0000002" || items[19].(map[string]any)["sha"] != "0000015" {
		t.Fatalf("commit append/dedupe/cap failed: %v", items)
	}
	registration["harness_session_ref"] = "metadata-ref-000000000000000001"
	registration["model"] = " gpt-6-sol "
	registration["account_label"] = " Codex\n Pro "
	w = f.call(f.person, "POST", base, registration, "")
	expect(t, w, 201)
	if decode(t, w)["id"] != id {
		t.Fatal("registration replay after metadata heartbeat created another session")
	}
	registration["model"] = "different-model"
	expect(t, f.call(f.person, "POST", base, registration, ""), 409)
	w = f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	if decode(t, w)["commits"].([]any)[19].(map[string]any)["subject"] != "New change" {
		t.Fatal("detail omitted latest commit")
	}
	w = f.call(f.person, "GET", "/api/harness-sessions/live", nil, "")
	expect(t, w, 200)
	live := decode(t, w)["items"].([]any)
	if len(live) != 1 || live[0].(map[string]any)["model"] != "gpt-6-sol" || live[0].(map[string]any)["reasoning_effort"] != "xhigh" || live[0].(map[string]any)["account_label"] != "CodexPro" || live[0].(map[string]any)["harness_version"] != "1.2.3" {
		t.Fatalf("live metadata mismatch: %v", live)
	}
	expect(t, f.call(f.foreign, "GET", path, nil, ""), 404)
	f.tx(t, f.foreign, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_sessions WHERE id=$1`, id).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("foreign tenant read session metadata")
		}
		return nil
	})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.heartbeat' AND after->>'id'=$1`, id).Scan(&count); err != nil {
			return err
		}
		if count != 2 {
			t.Fatalf("metadata heartbeats wrote %d events", count)
		}
		return nil
	})

	for _, update := range []map[string]any{
		{"account_label": strings.Repeat("界", 129)},
		{"commits": []map[string]string{{"sha": "bad", "subject": "invalid"}}},
		{"commits": []map[string]string{{"sha": "1234567", "subject": " \n "}}},
	} {
		update["phase"], update["activity_sequence"] = "working", 1
		expect(t, f.call(f.agent, "POST", path+"/heartbeat", update, lease), 400)
	}
}

// The account and model registry source bounds must fit session reporting.
func TestSessionAcceptsFullAccountCatalogLabels(t *testing.T) {
	f := fixture(t)
	label, model := strings.Repeat("界", 128), strings.Repeat("m", 128)
	registration := map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "host",
		"harness_session_ref": "catalog-metadata-reference", "worker_lease": "catalog-metadata-worker-lease-00000001",
		"management_mode": "unmanaged", "role": "worker", "model": model, "account_label": label,
	}
	w := f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", registration, "")
	expect(t, w, 201)
	got := decode(t, w)
	if got["account_label"] != label || got["model"] != model {
		t.Fatal("registry metadata was truncated")
	}
	path := "/api/projects/" + f.project + "/harness-sessions/" + got["id"].(string)
	update := map[string]any{"phase": "working", "activity_sequence": 1, "model": strings.Repeat("n", 128), "account_label": strings.Repeat("é", 128)}
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", update, registration["worker_lease"].(string)), 200)
	w = f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	detail := decode(t, w)
	history := detail["metadata_history"].([]any)
	if detail["account_label"] != update["account_label"] || detail["model"] != update["model"] || len(history) != 1 || history[0].(map[string]any)["value"] != update["model"] {
		t.Fatal("128-character metadata did not survive heartbeat, detail and model history")
	}
	registration["model"] = model + "x"
	registration["harness_session_ref"] = "catalog-metadata-reference-too-long"
	expect(t, f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", registration, ""), 400)
	registration["model"], registration["account_label"] = model, label+"x"
	expect(t, f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", registration, ""), 400)
}
