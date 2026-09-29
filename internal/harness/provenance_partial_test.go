// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import "testing"

// A heartbeat posts only root instruction files. A manual report can also
// name a skill and a prompt template. Either order must leave the kinds the
// later report does not carry on the current revision.
func TestProvenanceReportsReplaceOnlyTheKindsTheyCarry(t *testing.T) {
	const (
		agentsA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		agentsB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		claudeA = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		skillA  = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		promptV = "260927120000.0.0"
	)
	root := []any{
		map[string]any{"kind": "agents", "logical_name": "AGENTS.md", "hash_kind": "content", "content_sha256": agentsB, "byte_size": 12},
		map[string]any{"kind": "claude", "logical_name": "CLAUDE.md", "hash_kind": "content", "content_sha256": claudeA, "byte_size": 12},
	}
	manual := []any{
		map[string]any{"kind": "agents", "logical_name": "AGENTS.md", "hash_kind": "content", "content_sha256": agentsA, "byte_size": 20},
		map[string]any{"kind": "skill", "logical_name": "worker/SKILL.md", "hash_kind": "content", "content_sha256": skillA, "byte_size": 30},
		map[string]any{"kind": "prompt_template", "logical_name": "prompt-template", "hash_kind": "absent", "version": promptV},
	}
	t.Run("manual then heartbeat", func(t *testing.T) {
		got := postProvenanceOrders(t, manual, root)
		if got["AGENTS.md"] != agentsB || got["CLAUDE.md"] != claudeA || got["worker/SKILL.md"] != skillA || got["prompt-template"] != promptV {
			t.Fatalf("heartbeat dropped an earlier kind: %#v", got)
		}
	})
	t.Run("heartbeat then manual", func(t *testing.T) {
		got := postProvenanceOrders(t, root, manual)
		if got["AGENTS.md"] != agentsA || got["CLAUDE.md"] != claudeA || got["worker/SKILL.md"] != skillA || got["prompt-template"] != promptV {
			t.Fatalf("manual report dropped the heartbeat kind it did not carry: %#v", got)
		}
	})
}

func postProvenanceOrders(t *testing.T, first, second []any) map[string]string {
	t.Helper()
	f := fixture(t)
	lease := "partial-provenance-lease-00000000000001"
	w := f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "partial-host",
		"harness_session_ref": "partial-" + uid(), "worker_lease": lease,
		"management_mode": "unmanaged", "role": "worker",
	}, "")
	expect(t, w, 201)
	path := "/api/projects/" + f.project + "/harness-sessions/" + decode(t, w)["id"].(string) + "/provenance"
	expect(t, f.call(f.agent, "POST", path, map[string]any{"items": first}, lease), 200)
	expect(t, f.call(f.agent, "POST", path, map[string]any{"items": second}, lease), 200)
	page := decode(t, f.call(f.person, "GET", path, nil, ""))
	revisions := page["revisions"].([]any)
	if len(revisions) != 2 {
		t.Fatalf("revisions %#v", page)
	}
	out := map[string]string{}
	for _, raw := range revisions[0].(map[string]any)["items"].([]any) {
		item := raw.(map[string]any)
		name := item["logical_name"].(string)
		if item["content_sha256"] != nil {
			out[name] = item["content_sha256"].(string)
			continue
		}
		out[name] = item["version"].(string)
	}
	return out
}
