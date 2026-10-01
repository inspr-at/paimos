// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func piUsageFixture(at int64, model string, input, output, read, write int64) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "provider": "anthropic", "model": model, "timestamp": at,
		"usage": map[string]any{"input": input, "output": output, "cacheRead": read, "cacheWrite": write, "cost": map[string]any{"total": json.Number("0.0000075")}}}})
	return raw
}

func TestPiUsageAccumulatesCompletedMessagesPerModel(t *testing.T) {
	s := &piUsageTracker{}
	first := piUsageFixture(1, "model-one", 10, 4, 2, 3)
	ev, ok := s.event(first)
	if !ok || ev.InputTokensDelta != 15 || ev.OutputTokensDelta != 4 || ev.CachedInputTokensDelta != 2 || ev.CostMicrosDelta != 8 || ev.SessionUsage.Model != "anthropic/model-one" || ev.SessionUsage.ReasoningTokens != nil {
		t.Fatalf("first usage %+v", ev)
	}
	if _, ok := s.event(first); ok {
		t.Fatal("duplicate message was counted twice")
	}
	ev, ok = s.event(piUsageFixture(2, "model-one", 20, 5, 4, 1))
	if !ok || *ev.SessionUsage.InputTokens != 40 || *ev.SessionUsage.OutputTokens != 9 || *ev.SessionUsage.CachedInputTokens != 6 {
		t.Fatal("message totals did not accumulate")
	}
	ev, ok = s.event(piUsageFixture(3, "model-two", 3, 1, 0, 0))
	if !ok || ev.SessionUsage.Model != "anthropic/model-two" || *ev.SessionUsage.InputTokens != 3 {
		t.Fatal("model switch mixed counters")
	}
	for _, raw := range []json.RawMessage{
		[]byte(`{"type":"message_update","usage":{"input":500,"output":100}}`),
		[]byte(`{"type":"agent_end","messages":[]}`),
		[]byte(`{"type":"message_end","message":{"role":"toolResult","provider":"anthropic","model":"model-one","timestamp":4,"usage":{"input":100,"output":1,"cacheRead":0,"cacheWrite":0}}}`),
		[]byte(`{"type":"message_end","message":{"role":"assistant","provider":"anthropic","model":"model-one","timestamp":4,"usage":{"input":100,"output":1}}}`),
		piUsageFixture(4, "model-one", -1, 1, 0, 0),
		piUsageFixture(5, "model-one", managedUsageMax, 1, 1, 0),
		piUsageFixture(6, "model-one", managedUsageMax, 1, 0, 0),
	} {
		if _, ok := s.event(raw); ok {
			t.Fatal("unsupported or overflowing usage counted")
		}
	}
}

func TestGrokManagedUsageSeparatesCostAndCumulativeTokens(t *testing.T) {
	s := &grokUsageTracker{}
	raw := []byte(`{"update":{"sessionUpdate":"usage_update","used":100,"size":1000,"cost":{"amount":0.0000075,"currency":"USD"}}}`)
	ev, ok := s.event(raw, grokModel)
	if !ok || ev.SessionUsage != nil || ev.InputTokensDelta != 0 || ev.CostMicrosDelta != 8 {
		t.Fatal("cost-only update invented tokens")
	}
	if _, ok := s.event(raw, grokModel); ok {
		t.Fatal("duplicate cost counted")
	}
	raw = []byte(`{"update":{"sessionUpdate":"usage_update","inputTokens":11,"outputTokens":4,"cachedReadTokens":3,"reasoningTokens":2}}`)
	ev, ok = s.event(raw, grokModel)
	if !ok || ev.InputTokensDelta != 14 || ev.OutputTokensDelta != 4 || ev.CachedInputTokensDelta != 3 || ev.ReasoningTokensDelta != 2 {
		t.Fatalf("token deltas %+v", ev)
	}
	ev, ok = s.event(raw, grokModel)
	if !ok || ev.InputTokensDelta != 0 || ev.OutputTokensDelta != 0 || ev.CachedInputTokensDelta != 0 || ev.ReasoningTokensDelta != 0 {
		t.Fatal("cumulative replay doubled tokens")
	}
	ev, ok = s.event([]byte(`{"update":{"sessionUpdate":"usage_update","inputTokens":15,"outputTokens":7}}`), grokModel)
	if !ok || ev.InputTokensDelta != 4 || ev.OutputTokensDelta != 3 || *ev.SessionUsage.CachedInputTokens != 3 || *ev.SessionUsage.ReasoningTokens != 2 {
		t.Fatal("missing optional counters erased known values")
	}
	for _, raw := range []string{
		`{"update":{"sessionUpdate":"usage_update","inputTokens":14,"outputTokens":7}}`,
		`{"update":{"sessionUpdate":"usage_update","inputTokens":16,"outputTokens":8,"cachedReadTokens":-1}}`,
		`{"update":{"sessionUpdate":"usage_update","inputTokens":16,"outputTokens":8,"reasoningTokens":9}}`,
		`{"update":{"sessionUpdate":"usage_update","inputTokens":1000000000001,"outputTokens":8}}`,
	} {
		if ev, ok := s.event([]byte(raw), grokModel); ok || ev.SessionUsage != nil {
			t.Fatal("invalid or stale totals reported")
		}
	}
	ev, ok = (&grokUsageTracker{}).event([]byte(`{"update":{"sessionUpdate":"usage_update","inputTokens":2,"outputTokens":1}}`), grokModel)
	if !ok || ev.SessionUsage.CachedInputTokens != nil || ev.SessionUsage.ReasoningTokens != nil {
		t.Fatal("missing counters were invented")
	}
}

func TestGrokManagedUsageIncludesCacheReadAndCreation(t *testing.T) {
	s := &grokUsageTracker{}
	first := []byte(`{"update":{"sessionUpdate":"usage_update","inputTokens":2,"outputTokens":5,"cachedReadTokens":30,"cacheCreationTokens":4}}`)
	ev, ok := s.event(first, grokModel)
	if !ok || ev.SessionUsage == nil || *ev.SessionUsage.InputTokens != 36 || ev.InputTokensDelta != 36 || ev.CachedInputTokensDelta != 30 || ev.OutputTokensDelta != 5 {
		t.Fatalf("cache-heavy update dropped or miscounted: %+v", ev)
	}
	if ev, ok := s.event(first, grokModel); !ok || ev.InputTokensDelta != 0 || ev.CachedInputTokensDelta != 0 || ev.OutputTokensDelta != 0 {
		t.Fatal("inclusive totals were added again on replay")
	}
	ev, ok = s.event([]byte(`{"update":{"sessionUpdate":"usage_update","inputTokens":5,"outputTokens":7,"cachedReadTokens":40,"cacheCreationTokens":6}}`), grokModel)
	if !ok || *ev.SessionUsage.InputTokens != 51 || ev.InputTokensDelta != 15 || ev.CachedInputTokensDelta != 10 || ev.OutputTokensDelta != 2 {
		t.Fatal("cache-inclusive cumulative increase was miscounted")
	}
	// Missing optional counters retain their last observed cumulative values
	// in the inclusive input as well as in the cached subset.
	ev, ok = s.event([]byte(`{"update":{"sessionUpdate":"usage_update","inputTokens":7,"outputTokens":8}}`), grokModel)
	if !ok || *ev.SessionUsage.InputTokens != 53 || ev.InputTokensDelta != 2 || *ev.SessionUsage.CachedInputTokens != 40 || ev.CachedInputTokensDelta != 0 {
		t.Fatal("missing optional cache totals changed inclusive accounting")
	}
	for _, raw := range []string{
		`{"update":{"sessionUpdate":"usage_update","inputTokens":8,"outputTokens":9,"cachedReadTokens":-1}}`,
		`{"update":{"sessionUpdate":"usage_update","inputTokens":8,"outputTokens":9,"cacheCreationTokens":-1}}`,
		`{"update":{"sessionUpdate":"usage_update","inputTokens":8,"outputTokens":9,"cachedReadTokens":40,"cacheCreationTokens":5}}`,
		`{"update":{"sessionUpdate":"usage_update","inputTokens":999999999999,"outputTokens":9,"cachedReadTokens":1,"cacheCreationTokens":1}}`,
		`{"update":{"sessionUpdate":"usage_update","inputTokens":1,"outputTokens":9,"cachedReadTokens":9223372036854775807}}`,
		`{"update":{"sessionUpdate":"usage_update","inputTokens":1,"outputTokens":9,"cacheCreationTokens":9223372036854775807}}`,
	} {
		if ev, ok := s.event([]byte(raw), grokModel); ok || ev.SessionUsage != nil {
			t.Fatal("invalid or overflowing inclusive counters reported")
		}
	}
	if *s.models[grokModel].InputTokens != 53 {
		t.Fatal("rejected update changed the accepted totals")
	}
	ev, ok = s.event([]byte(`{"update":{"sessionUpdate":"usage_update","inputTokens":1,"outputTokens":2,"cachedReadTokens":20,"cacheCreationTokens":1,"model":"other-model"}}`), grokModel)
	if !ok || ev.SessionUsage.Model != "other-model" || ev.InputTokensDelta != 22 || ev.CachedInputTokensDelta != 20 {
		t.Fatal("inclusive cache accounting mixed models")
	}
}

func TestGrokManagedUsageRetainsFallingReasoning(t *testing.T) {
	s := &grokUsageTracker{}
	for _, tc := range []struct {
		name, raw                            string
		input, output, cached, reasoning     int64
		inputDelta, outputDelta, cachedDelta int64
		reasoningDelta                       int64
	}{
		{"initial", `{"update":{"sessionUpdate":"usage_update","inputTokens":2,"outputTokens":8,"cachedReadTokens":30,"cacheCreationTokens":4,"reasoningTokens":6}}`, 36, 8, 30, 6, 36, 8, 30, 6},
		{"reasoning-revised-down", `{"update":{"sessionUpdate":"usage_update","inputTokens":5,"outputTokens":10,"cachedReadTokens":40,"cacheCreationTokens":6,"reasoningTokens":3}}`, 51, 10, 40, 6, 15, 2, 10, 0},
		{"replay", `{"update":{"sessionUpdate":"usage_update","inputTokens":5,"outputTokens":10,"cachedReadTokens":40,"cacheCreationTokens":6,"reasoningTokens":3}}`, 51, 10, 40, 6, 0, 0, 0, 0},
		{"missing-reasoning", `{"update":{"sessionUpdate":"usage_update","inputTokens":7,"outputTokens":11}}`, 53, 11, 40, 6, 2, 1, 0, 0},
		{"reasoning-grows-again", `{"update":{"sessionUpdate":"usage_update","inputTokens":9,"outputTokens":13,"cachedReadTokens":41,"cacheCreationTokens":7,"reasoningTokens":9}}`, 57, 13, 41, 9, 4, 2, 1, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := s.event([]byte(tc.raw), grokModel)
			if !ok || ev.SessionUsage == nil {
				t.Fatal("valid usage update was dropped")
			}
			report := ev.SessionUsage
			if usageCount(report.InputTokens) != tc.input || usageCount(report.OutputTokens) != tc.output || usageCount(report.CachedInputTokens) != tc.cached || report.ReasoningTokens == nil || *report.ReasoningTokens != tc.reasoning {
				t.Fatalf("wrong cumulative usage: %+v", report)
			}
			if ev.InputTokensDelta != tc.inputDelta || ev.OutputTokensDelta != tc.outputDelta || ev.CachedInputTokensDelta != tc.cachedDelta || ev.ReasoningTokensDelta != tc.reasoningDelta {
				t.Fatalf("wrong telemetry deltas: %+v", ev)
			}
		})
	}
}

func TestPiAdapterPublishesUsageFromOwnedWire(t *testing.T) {
	r := adapterRequest(t)
	r.Profile.Harness, r.Profile.Model = Pi, "anthropic/test-model"
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	a := NewPiAdapter(fakeVendorPath(t, "pi_usage"), map[string]string{"account": home})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan AdapterEvent, 16)
	p, err := a.Start(ctx, r, func(ev AdapterEvent) { events <- ev })
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop(ctx)
	for {
		select {
		case ev := <-events:
			if ev.SessionUsage != nil {
				if ev.SessionUsage.Model != r.Profile.Model || ev.InputTokensDelta != 15 || ev.CostMicrosDelta != 8 {
					t.Fatal("Pi wire usage lost")
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("Pi wire usage not observed")
		}
	}
}

func TestCursorAdapterAcceptsExplicitPromptTokens(t *testing.T) {
	r := adapterRequest(t)
	r.Profile.Harness = Cursor
	a := NewCursorAdapter(fakeVendorPath(t, "cursor_usage"), map[string]string{"account": "42"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan AdapterEvent, 16)
	p, err := a.Start(ctx, r, func(ev AdapterEvent) { events <- ev })
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop(ctx)
	for {
		select {
		case ev := <-events:
			if ev.SessionUsage != nil {
				if ev.InputTokensDelta != 15 || ev.OutputTokensDelta != 4 || ev.CachedInputTokensDelta != 2 || ev.ReasoningTokensDelta != 1 {
					t.Fatal("Cursor prompt usage lost")
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("Cursor prompt usage not observed")
		}
	}
}
