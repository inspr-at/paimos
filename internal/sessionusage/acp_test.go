// SPDX-License-Identifier: AGPL-3.0-only
package sessionusage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestACPThroughputSeparatesCacheAndReasoning(t *testing.T) {
	for _, tc := range []struct{ source, raw string }{
		{"gemini", `{"inputTokens":15,"outputTokens":4,"cachedReadTokens":5,"thoughtTokens":3,"totalTokens":19}`},
		{"opencode", `{"inputTokens":8,"outputTokens":4,"cachedReadTokens":5,"cachedWriteTokens":2,"thoughtTokens":3,"totalTokens":22}`},
	} {
		r, ok := ACPPromptUsage(tc.source, json.RawMessage(tc.raw), "vendor/model")
		if !ok || *r.InputTokens != 15 || *r.OutputTokens != 7 || *r.CachedInputTokens != 5 || *r.ReasoningTokens != 3 || !r.Provisional {
			t.Fatalf("%s: %+v %v", tc.source, r, ok)
		}
	}
	for _, raw := range []string{`{"used":100,"size":1000}`, `{"inputTokens":1,"outputTokens":2,"totalTokens":4}`, `{"inputTokens":1,"outputTokens":2,"totalTokens":3,"cachedReadTokens":2}`, `{"inputTokens":1,"outputTokens":2,"totalTokens":3,"cost":1}`, `{"inputTokens":1,"inputTokens":2,"outputTokens":2,"totalTokens":3}`, `{"inputTokens":-1,"outputTokens":2,"totalTokens":1}`} {
		if _, ok := ACPPromptUsage("gemini", json.RawMessage(raw), "gemini-2.5-pro"); ok {
			t.Fatal("invalid usage accepted", raw)
		}
	}
	if _, ok := ACPPromptUsage("opencode", json.RawMessage(`{"inputTokens":1,"outputTokens":2,"totalTokens":3}`), ""); ok {
		t.Fatal("missing model guessed")
	}
}

func TestNewHarnessNativeCapturesDedupeAndBind(t *testing.T) {
	opt := Options{Source: "opencode", SessionID: "12345678-1234-1234-1234-123456789abc", SourceSessionID: "ses_1", FromStart: true, Model: "ollama/qwen3-coder"}
	one := `{"type":"step_finish","sessionID":"ses_1","part":{"id":"prt_1","sessionID":"ses_1","tokens":{"input":8,"output":4,"reasoning":3,"cache":{"read":5,"write":2},"total":22},"cost":0.001}}` + "\n"
	r, err := Parse(strings.NewReader(one+one), opt)
	if err != nil || len(r.Reports) != 1 || *r.Reports[0].InputTokens != 15 || *r.Reports[0].OutputTokens != 7 {
		t.Fatalf("dedupe: %+v %v", r, err)
	}
	second := strings.Replace(one, "prt_1", "prt_2", 1)
	opt.Previous = &r.Checkpoint
	opt.FromStart = false
	next, err := Parse(strings.NewReader(one+one+second), opt)
	if err != nil || *next.Reports[0].InputTokens != 30 {
		t.Fatal("continuity", next, err)
	}
	for _, raw := range []string{strings.Replace(one, `"sessionID":"ses_1"`, `"sessionID":"foreign"`, 1), strings.Replace(one, `"sessionID":"ses_1","tokens"`, `"sessionID":"foreign","tokens"`, 1), strings.Replace(one, `"total":22`, `"total":23`, 1), strings.Replace(one, `"cost":0.001`, `"cost":-1`, 1)} {
		opt.Previous = nil
		opt.FromStart = true
		if _, err := Parse(strings.NewReader(raw), opt); err == nil {
			t.Fatal("unsafe capture accepted", raw)
		}
	}
	opt.Source = "gemini"
	opt.Model = "gemini-2.5-pro"
	opt.Previous = nil
	opt.FromStart = true
	native := `{"session_id":"ses_1","stats":{"models":{"gemini-2.5-pro":{"tokens":{"input":10,"prompt":15,"candidates":4,"thoughts":3,"cached":5,"total":22,"tool":0}}}}}` + "\n"
	r, err = Parse(strings.NewReader(native), opt)
	if err != nil || len(r.Reports) != 1 || *r.Reports[0].InputTokens != 15 || *r.Reports[0].OutputTokens != 7 {
		t.Fatal("Gemini native", r, err)
	}
	if _, ok, err := ParseHeartbeatLine("gemini", opt.Model, []byte(native)); err != nil || !ok {
		t.Fatal("heartbeat did not share schema", ok, err)
	}
	if _, ok, _ := ParseHeartbeatLine("opencode", "ollama/qwen3-coder", []byte(`{"method":"session/update","params":{"update":{"sessionUpdate":"usage_update","used":100,"size":1000}}}`)); ok {
		t.Fatal("context occupancy became throughput")
	}
}
