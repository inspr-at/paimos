// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"strings"
	"testing"
)

func TestHeartbeatCommitDiffValidationAndStorage(t *testing.T) {
	f := fixture(t)
	lease := "diff-lease-0000000000000000000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test", "management_mode": "unmanaged", "role": "worker",
		"harness_session_ref": "diff-ref-00000000000000000000000001", "worker_lease": lease,
	}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id + "/heartbeat"
	for _, counts := range []map[string]any{
		{"lines_added": 1},
		{"lines_added": 1, "lines_deleted": 2, "files_changed": -1},
		{"lines_added": 1000000001, "lines_deleted": 0, "files_changed": 1},
	} {
		counts["sha"] = "abcdef0"
		counts["subject"] = "Diff"
		rejected := f.call(f.agent, "POST", path, map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1, "commits": []map[string]any{counts}}, lease)
		expect(t, rejected, 400)
		if !strings.Contains(rejected.Body.String(), "commit diff") {
			t.Fatalf("wrong failure reason: %s", rejected.Body.String())
		}
	}
	w = f.call(f.agent, "POST", path, map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1, "commits": []map[string]any{{"sha": "abcdef0", "subject": "Diff", "lines_added": 5, "lines_deleted": 2, "files_changed": 1}}}, lease)
	expect(t, w, 200)
	commits := decode(t, w)["commits"].([]any)
	if len(commits) != 1 {
		t.Fatal("failed writes stored commits", commits)
	}
	c := commits[0].(map[string]any)
	if c["lines_added"] != float64(5) || c["lines_deleted"] != float64(2) || c["files_changed"] != float64(1) {
		t.Fatal("diff evidence lost", c)
	}
}
