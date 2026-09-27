// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func managedFrame(model string, input, output, cached int64, last string) []byte {
	cache := ""
	if cached >= 0 {
		cache = fmt.Sprintf(`,"cachedInputTokens":%d`, cached)
	}
	modelField := ""
	if model != "" {
		modelField = fmt.Sprintf(`,"model":%q`, model)
	}
	lastField := ""
	if last != "" {
		lastField = `,"last":` + last
	}
	return []byte(fmt.Sprintf(`{"method":"thread/tokenUsage/updated","params":{"threadId":"synthetic-thread"%s,"tokenUsage":{"total":{"inputTokens":%d,"outputTokens":%d%s}%s}}}`, modelField, input, output, cache, lastField))
}

func managedCapture(t *testing.T) *ManagedCodex {
	t.Helper()
	c, err := NewManagedCodex("synthetic-thread", "model-a")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestManagedCodexCumulativeReplayAndModelChanges(t *testing.T) {
	c := managedCapture(t)
	frames := [][]byte{
		managedFrame("", 100, 20, 30, ""),
		managedFrame("model-b", 150, 28, 40, `{"inputTokens":50,"outputTokens":8,"cachedInputTokens":10}`),
		managedFrame("model-b", 150, 28, 40, `{"inputTokens":50,"outputTokens":8,"cachedInputTokens":10}`),
		managedFrame("model-a", 180, 32, 45, `{"inputTokens":30,"outputTokens":4,"cachedInputTokens":5}`),
	}
	for i, raw := range frames {
		r, err := c.Observe(raw)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if i == 2 && r != nil {
			t.Fatal("duplicate created usage")
		}
	}
	final := c.Finish(true)
	if len(final) != 2 || final[0].Model != "model-a" || final[1].Model != "model-b" {
		t.Fatalf("models: %+v", final)
	}
	if *final[0].InputTokens != 130 || *final[0].OutputTokens != 24 || *final[0].CachedInputTokens != 35 ||
		*final[1].InputTokens != 50 || *final[1].OutputTokens != 8 || *final[1].CachedInputTokens != 10 || final[0].Provisional || final[1].Provisional {
		t.Fatal("thread total counted twice or model settlement wrong")
	}
	if _, err := c.Observe(frames[0]); err == nil {
		t.Fatal("extended final capture")
	}
	if len(c.Finish(true)) != 0 {
		t.Fatal("final repeated")
	}
}

func TestManagedCodexUnknownCacheAndIncompleteRemainProvisional(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cache int64
		clean bool
	}{
		{"unknown", -1, true}, {"interrupted", 10, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := managedCapture(t)
			r, err := c.Observe(managedFrame("", 100, 20, tc.cache, ""))
			if err != nil || !r.Provisional {
				t.Fatalf("first: %v", err)
			}
			final := c.Finish(tc.clean)
			if len(final) != 1 || !final[0].Provisional || (final[0].CachedInputTokens == nil) != (tc.cache < 0) {
				t.Fatal("unknown or interrupted usage finalized")
			}
		})
	}
	c := managedCapture(t)
	for _, raw := range [][]byte{managedFrame("", 100, 20, -1, ""), managedFrame("", 150, 30, 15, ""), managedFrame("", 200, 40, 25, "")} {
		if _, err := c.Observe(raw); err != nil {
			t.Fatal(err)
		}
	}
	if c.Finish(true)[0].CachedInputTokens != nil {
		t.Fatal("invented missing cache attribution")
	}
}

func TestManagedCodexRejectsUnsafeCapture(t *testing.T) {
	base := string(managedFrame("", 100, 20, 30, ""))
	for name, raw := range map[string]string{
		"wrong thread":              strings.Replace(base, "synthetic-thread", "another-thread", 1),
		"missing identity":          strings.Replace(base, `"threadId":"synthetic-thread",`, "", 1),
		"conflicting identity":      strings.Replace(base, `"threadId":`, `"thread_id":"other","threadId":`, 1),
		"duplicate counter":         strings.Replace(base, `"inputTokens":100`, `"inputTokens":100,"inputTokens":101`, 1),
		"negative":                  strings.Replace(base, `"inputTokens":100`, `"inputTokens":-1`, 1),
		"overflow":                  strings.Replace(base, `"inputTokens":100`, `"inputTokens":1000000000001`, 1),
		"fraction":                  strings.Replace(base, `"inputTokens":100`, `"inputTokens":1.5`, 1),
		"unknown field":             strings.Replace(base, `"inputTokens":100`, `"inputTokens":100,"cost":1`, 1),
		"cache exceeds input":       strings.Replace(base, `"cachedInputTokens":30`, `"cachedInputTokens":101`, 1),
		"total mismatch":            strings.Replace(base, `"inputTokens":100`, `"totalTokens":1,"inputTokens":100`, 1),
		"unknown model":             string(managedFrame("auto", 100, 20, 30, "")),
		"bound":                     strings.Repeat("x", maxLine+1),
		"model switch without last": string(managedFrame("model-b", 150, 28, 40, "")),
		"model switch partial last": string(managedFrame("model-b", 150, 28, 40, `{"inputTokens":20,"outputTokens":8,"cachedInputTokens":10}`)),
	} {
		t.Run(name, func(t *testing.T) {
			c := managedCapture(t)
			if _, err := c.Observe(managedFrame("", 50, 10, 10, "")); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Observe([]byte(raw)); err == nil {
				t.Fatal("unsafe capture accepted")
			}
			if _, err := c.Observe([]byte(base)); err == nil {
				t.Fatal("failed capture resumed")
			}
			final := c.Finish(true)
			if len(final) != 1 || !final[0].Provisional || *final[0].InputTokens != 50 {
				t.Fatal("bad frame changed/finalized report")
			}
		})
	}
	for name, raw := range map[string][]byte{
		"decrease":          managedFrame("", 99, 21, 30, ""),
		"cache decrease":    managedFrame("", 110, 21, 29, ""),
		"uncached decrease": managedFrame("", 110, 21, 50, ""),
		"known to unknown":  managedFrame("", 110, 21, -1, ""),
	} {
		t.Run(name, func(t *testing.T) {
			c := managedCapture(t)
			_, _ = c.Observe([]byte(base))
			if _, err := c.Observe(raw); err == nil {
				t.Fatal("decreasing known counter accepted")
			}
		})
	}
}

func TestManagedCodexDiscardsTextAndBoundsModels(t *testing.T) {
	c := managedCapture(t)
	raw := strings.Replace(string(managedFrame("", 100, 20, 30, "")), `"params":`, `"prompt":"synthetic text marker","tool":{"output":"synthetic text marker"},"params":`, 1)
	r, err := c.Observe([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "synthetic") || strings.Contains(string(b), "thread") {
		t.Fatal("vendor text/identity leaked")
	}
	for i := 1; i < 128; i++ {
		_, err = c.Observe(managedFrame(fmt.Sprintf("model-%d", i), int64(100+i), 20, 30, `{"inputTokens":1,"outputTokens":0,"cachedInputTokens":0}`))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = c.Observe(managedFrame("extra", 228, 20, 30, `{"inputTokens":1,"outputTokens":0,"cachedInputTokens":0}`)); err == nil {
		t.Fatal("unbounded models")
	}
}

func TestManagedCodexNativeRerouteNeedsMatchingInterval(t *testing.T) {
	reroute := []byte(`{"method":"model/rerouted","params":{"threadId":"synthetic-thread","turnId":"synthetic-turn","fromModel":"model-a","toModel":"model-b","reason":"synthetic ignored reason"}}`)
	next := strings.Replace(string(managedFrame("", 150, 28, 40, `{"inputTokens":50,"outputTokens":8,"cachedInputTokens":10}`)), `"threadId":`, `"turnId":"synthetic-turn","threadId":`, 1)
	for _, tc := range []struct {
		name, frame string
		wantError   bool
	}{
		{"bound request", next, false},
		{"wrong turn", strings.Replace(next, "synthetic-turn", "other-turn", 1), true},
		{"conflicting usage model", strings.Replace(next, `"turnId":`, `"model":"model-a","turnId":`, 1), true},
		{"partial interval", strings.Replace(next, `"inputTokens":50`, `"inputTokens":49`, 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := managedCapture(t)
			_, _ = c.Observe(managedFrame("", 100, 20, 30, ""))
			for range 2 {
				if r, err := c.Observe(reroute); err != nil || r != nil {
					t.Fatalf("reroute/replay: %v", err)
				}
			}
			r, err := c.Observe([]byte(tc.frame))
			if (err != nil) != tc.wantError {
				t.Fatalf("rerouted interval: %v", err)
			}
			if !tc.wantError && (r.Model != "model-b" || *r.InputTokens != 50) {
				t.Fatal("reroute counted entire thread or wrong model")
			}
			final := c.Finish(true)
			if final[0].Provisional != tc.wantError {
				t.Fatal("reroute settlement truthfulness")
			}
		})
	}
	for _, consume := range []bool{false, true} {
		c := managedCapture(t)
		_, _ = c.Observe(managedFrame("", 100, 20, 30, ""))
		_, _ = c.Observe(reroute)
		if consume {
			_, _ = c.Observe([]byte(next))
			if _, err := c.Observe(managedFrame("", 170, 30, 40, "")); err == nil {
				t.Fatal("guessed whether request-scoped reroute persisted")
			}
		}
		for _, r := range c.Finish(true) {
			if !r.Provisional {
				t.Fatal("unresolved model attribution finalized")
			}
		}
	}
}
