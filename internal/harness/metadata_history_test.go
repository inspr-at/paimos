// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestHeartbeatLabelAndMetadataHistory(t *testing.T) {
	f := fixture(t)
	lease := "metadata-history-lease-00000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local",
		"harness_session_ref": "metadata-history-ref-000000000001", "worker_lease": lease,
		"management_mode": "unmanaged", "role": "worker", "display_label": "Original",
		"model": "model-a", "reasoning_effort": "high",
	}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	beat := func(seq int, extra map[string]any) map[string]any {
		t.Helper()
		body := map[string]any{"phase": "working", "activity": "busy", "activity_sequence": seq}
		for key, value := range extra {
			body[key] = value
		}
		w := f.call(f.agent, "POST", path+"/heartbeat", body, lease)
		expect(t, w, 200)
		return decode(t, w)
	}
	for _, bad := range []any{strings.Repeat("界", 129), "bad\nlabel", 123} {
		expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "display_label": bad}, lease), 400)
	}
	if got := beat(1, map[string]any{"display_label": "  New name  ", "model": "model-b", "reasoning_effort": "xhigh"}); got["display_label"] != "New name" || got["model"] != "model-b" || got["reasoning_effort"] != "xhigh" {
		t.Fatalf("heartbeat metadata update: %v", got)
	}
	beat(1, map[string]any{"display_label": "New name", "model": "model-b", "reasoning_effort": "xhigh"})
	beat(2, nil)
	detail := decode(t, f.call(f.person, "GET", path, nil, ""))
	history := detail["metadata_history"].([]any)
	if len(history) != 3 {
		t.Fatalf("repeated or omitted metadata made changes: %v", history)
	}
	want := []struct{ field, old, next string }{{"reasoning_effort", "high", "xhigh"}, {"model", "model-a", "model-b"}, {"display_label", "Original", "New name"}}
	for i, item := range history {
		change := item.(map[string]any)
		if change["field"] != want[i].field || change["previous_value"] != want[i].old || change["value"] != want[i].next || change["at"] == nil {
			t.Fatalf("history[%d] = %v", i, change)
		}
	}
	beat(3, map[string]any{"display_label": nil})
	if got := decode(t, f.call(f.person, "GET", path, nil, ""))["display_label"]; got != nil {
		t.Fatalf("null did not clear label: %v", got)
	}
	beat(4, map[string]any{"display_label": "Reborn"})
	beat(5, map[string]any{"display_label": "  "})
	for seq := 6; seq <= 27; seq++ {
		beat(seq, map[string]any{"display_label": fmt.Sprintf("Name %02d", seq)})
	}
	detail = decode(t, f.call(f.person, "GET", path, nil, ""))
	history = detail["metadata_history"].([]any)
	if detail["display_label"] != "Name 27" || len(history) != 20 || history[0].(map[string]any)["value"] != "Name 27" || history[19].(map[string]any)["value"] != "Name 08" {
		t.Fatalf("bounded metadata history: %v", history)
	}
	page := decode(t, f.call(f.person, "GET", "/api/harness-sessions", nil, ""))["items"].([]any)
	if len(page) != 1 || page[0].(map[string]any)["display_label"] != "Name 27" {
		t.Fatalf("list did not expose current name: %v", page)
	}
	expect(t, f.call(f.foreign, "GET", path, nil, ""), 404)
	f.tx(t, f.foreign, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_metadata_changes WHERE session_id=$1`, id).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("foreign tenant saw %d metadata changes", count)
		}
		return nil
	})
}
