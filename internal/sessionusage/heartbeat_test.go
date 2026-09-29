// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHeartbeatFixtures(t *testing.T) {
	codex, err := os.ReadFile(filepath.Join("testdata", "codex_token_count.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var last HeartbeatLine
	for _, line := range splitLines(codex) {
		got, ok, err := ParseHeartbeatLine("codex", "gpt-5", line)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			last = got
		}
	}
	if !last.Absolute || last.Model != "gpt-5" || last.Input != 150 || last.Output != 35 || last.Cached != 80 {
		t.Fatalf("codex cumulative: %+v", last)
	}
	cursor, err := os.ReadFile(filepath.Join("testdata", "cursor_result.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var deltas []HeartbeatLine
	for _, line := range splitLines(cursor) {
		got, ok, err := ParseHeartbeatLine("cursor", "", line)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			deltas = append(deltas, got)
		}
	}
	if len(deltas) != 1 || deltas[0].Absolute || deltas[0].Model != "composer-2.5" || deltas[0].Input != 42 || deltas[0].Cached != 10 || deltas[0].Output != 8 || deltas[0].ID == "" {
		t.Fatalf("cursor delta: %+v", deltas)
	}
	grok, err := os.ReadFile(filepath.Join("testdata", "grok_usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	lines, err := ParseGrokUsage(grok, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]HeartbeatLine{}
	for _, line := range lines {
		got[line.Model] = line
	}
	if len(got) != 2 || !got["grok-4"].Absolute || got["grok-4"].Input != 88 || got["grok-4"].Cached != 7 || got["grok-4"].Output != 15 || got["grok-4-fast"].Input != 25 || got["grok-4-fast"].Cached != 3 || got["grok-4-fast"].Output != 5 {
		t.Fatalf("grok models: %+v", got)
	}
	sessionOnly, err := ParseGrokUsage([]byte(`{"session":{"inputTokens":10,"outputTokens":4,"cachedReadTokens":2,"cacheCreationTokens":1,"primaryModelId":"grok-4","costUsdTicks":99}}`), "")
	if err != nil || len(sessionOnly) != 1 || sessionOnly[0].Input != 13 || sessionOnly[0].Cached != 2 || sessionOnly[0].Output != 4 || sessionOnly[0].Model != "grok-4" {
		t.Fatalf("grok session total: %+v %v", sessionOnly, err)
	}
	costOnly := map[string]json.RawMessage{
		"used": json.RawMessage(`1`),
		"cost": json.RawMessage(`{"amount":1}`),
	}
	if _, ok := CursorPromptUsage(costOnly, "composer-2.5"); ok {
		t.Fatal("cost-only cursor update became tokens")
	}
	auth := []byte(`{"type":"event_msg","payload":{"type":"token_count"}}`)
	if _, ok, err := ParseHeartbeatLine("codex", "gpt-5", auth); ok || err != nil {
		t.Fatal("incomplete token_count was reported")
	}
}

func TestCodexTurnContextModel(t *testing.T) {
	contextLine := []byte(`{"type":"turn_context","payload":{"model":"gpt-5.4"}}`)
	usageLine := []byte(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150,"cached_input_tokens":80,"output_tokens":35,"reasoning_output_tokens":4,"total_tokens":185}}}}`)
	model, ok := CodexContextModel(contextLine)
	if !ok || model != "gpt-5.4" {
		t.Fatalf("context model %q ok %v", model, ok)
	}
	if _, contextUsage := CodexContextModel(usageLine); contextUsage {
		t.Fatal("token_count was treated as turn_context")
	}
	got, ok, err := ParseHeartbeatLine("codex", "", usageLine)
	if err != nil || ok {
		t.Fatalf("model-less rollout without context: ok %v err %v", ok, err)
	}
	got, ok, err = ParseHeartbeatLine("codex", model, usageLine)
	if err != nil || !ok || got.Model != "gpt-5.4" || got.Input != 150 || got.Output != 35 || got.Cached != 80 {
		t.Fatalf("turn_context attribution: %+v ok %v err %v", got, ok, err)
	}
	got, ok, err = ParseHeartbeatLine("codex", "gpt-5", usageLine)
	if err != nil || !ok || got.Model != "gpt-5" {
		t.Fatalf("explicit fallback still applies when no context is passed: %+v", got)
	}
}

func splitLines(raw []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range raw {
		if b == '\n' {
			if i > start {
				lines = append(lines, raw[start:i])
			}
			start = i + 1
		}
	}
	if start < len(raw) {
		lines = append(lines, raw[start:])
	}
	return lines
}
