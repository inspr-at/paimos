// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestVendorLimitMappings(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	no := false
	known := []Reading{{WindowKind: "5h", Bucket: "codex", WindowMinutes: 300, UsedPercent: 42, ResetsAt: reset, ReadAt: now, Source: "harness", OrdinaryUsageAllowed: &no}}
	for _, tc := range []struct {
		vendor, raw string
		bounded     bool
	}{
		{"codex", `{"method":"account/rateLimits/updated","params":{"ordinaryUsageAllowed":false}}`, false},
		{"codex", `{"method":"error","params":{"error":{"codexErrorInfo":"usageLimitExceeded"}}}`, false},
		{"claude", fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":%d}}`, reset.Unix()), true},
		{"grok", `{"error":{"error_type":"usage_limit_reached"}}`, false},
		{"grok", `{"error":{"error_type":"rate_limited"}}`, false},
		{"grok", `{"error_type":"usage_pool_exhausted"}`, false},
		{"grok", `{"error":{"error_type":"global_rate_limit"}}`, false},
		{"pi", `{"error":{"code":"rate_limit_exceeded"}}`, false},
	} {
		t.Run(tc.vendor+tc.raw, func(t *testing.T) {
			var rs []Reading
			if tc.vendor == "codex" {
				rs = known
			}
			hit := VendorLimit(tc.vendor, []byte(tc.raw), rs, now)
			if hit == nil {
				t.Fatal("limit not recognized")
			}
			if tc.bounded {
				if len(hit.Readings) != 1 || hit.Readings[0].UsedPercent != 100 || hit.Readings[0].Source != "harness" || hit.ResetsAt == nil || !hit.ResetsAt.Equal(reset) {
					t.Fatal("missing normalized limit reading")
				}
			}
			if !tc.bounded && (len(hit.Readings) != 0 || hit.ResetsAt != nil) {
				t.Fatal("invented limit bounds")
			}
		})
	}
	for _, raw := range []string{
		`{"message":"rate_limited"}`,
		`{"error":{"message":"usage_limit_reached"}}`,
		`{"error":{"code":"auth_failed"}}`,
		`{"ordinaryUsageAllowed":true}`,
		`{"method":"turn/failed","params":{"error":{"codexErrorInfo":{"rateLimitReachedType":"usage"}}}}`,
		`{"error":{"code":"usage_limit_reached","data":{"window_minutes":300,"resets_at":1}}}`,
		`{"error":{"data":{"type":"usage_pool_exhausted"}}}`,
		`{"error":{"error_type":"concurrency_limit"}}`,
		`{"error":{"code":429}}`,
		`{"error":{"code":-32000,"data":{"code":"quota_exceeded"}}}`,
	} {
		for _, vendor := range []string{"codex", "claude", "grok", "cursor", "pi"} {
			if VendorLimit(vendor, []byte(raw), known, now) != nil {
				t.Fatalf("%s classified as quota: %s", vendor, raw)
			}
		}
	}
}

func TestVendorLimitNamesOnlyTheWindowTheVendorNamed(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	five := now.Add(2 * time.Hour)
	week := now.Add(4 * 24 * time.Hour)
	no := false
	known := []Reading{
		{WindowKind: "5h", Bucket: "codex", WindowMinutes: 300, UsedPercent: 100, ResetsAt: five, ReadAt: now, Source: "harness", OrdinaryUsageAllowed: &no},
		{WindowKind: "weekly", Bucket: "codex", WindowMinutes: 10080, UsedPercent: 40, ResetsAt: week, ReadAt: now, Source: "harness", OrdinaryUsageAllowed: &no},
	}
	hit := VendorLimit("codex", []byte(`{"rateLimits":{"primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1},"secondary":{"usedPercent":40,"windowDurationMins":10080,"resetsAt":1}},"ordinaryUsageAllowed":false}`), known, now)
	if hit == nil || hit.Window != "" || hit.ResetsAt != nil || len(hit.Readings) != 0 {
		t.Fatalf("unnamed stop rewrote windows: %+v", hit)
	}
	raw := fmt.Sprintf(`{"rateLimits":{"rateLimitReachedType":"rate_limit_reached","primary":{"usedPercent":21,"windowDurationMins":300,"resetsAt":%d},"secondary":{"usedPercent":40,"windowDurationMins":10080,"resetsAt":%d}}}`, five.Unix(), week.Unix())
	hit = VendorLimit("codex", []byte(raw), known, now)
	if hit == nil || hit.Window != "" || hit.ResetsAt != nil || len(hit.Readings) != 0 {
		t.Fatalf("snapshot reach named a window: %+v", hit)
	}
	raw = fmt.Sprintf(`{"rateLimits":{"primary":{"usedPercent":80,"windowDurationMins":300,"resetsAt":%d,"rateLimitReachedType":"rate_limit_reached"},"secondary":{"usedPercent":40,"windowDurationMins":10080,"resetsAt":%d}}}`, five.Unix(), week.Unix())
	hit = VendorLimit("codex", []byte(raw), known, now)
	if hit == nil || hit.Window != "5h" || hit.ResetsAt == nil || !hit.ResetsAt.Equal(five) || len(hit.Readings) != 1 || hit.Readings[0].UsedPercent != 100 || hit.Readings[0].WindowKind != "5h" {
		t.Fatalf("primary window was not the only full window: %+v", hit)
	}
	raw = fmt.Sprintf(`{"rateLimitsByLimitId":{"codex":{"secondary":{"windowDurationMins":10080,"resetsAt":%d,"rateLimitReachedType":"workspace_owner_usage_limit_reached"}}}}`, week.Unix())
	hit = VendorLimit("codex", []byte(raw), known, now)
	if hit == nil || hit.Window != "weekly" || len(hit.Readings) != 1 || hit.Readings[0].UsedPercent != 100 || hit.Readings[0].WindowMinutes != 10080 {
		t.Fatalf("per-limit window missing: %+v", hit)
	}
	stop := now.Add(3 * time.Hour)
	raw = fmt.Sprintf(`{"error":{"codexErrorInfo":{"type":"usageLimitExceeded","limitId":"codex","resetsAt":%d}}}`, stop.Unix())
	hit = VendorLimit("codex", []byte(raw), known, now)
	if hit == nil || hit.Window != "" || len(hit.Readings) != 0 || hit.ResetsAt == nil || !hit.ResetsAt.Equal(stop) {
		t.Fatalf("limit id invented a window: %+v", hit)
	}
	raw = fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":%d}}`, stop.Unix())
	hit = VendorLimit("claude", []byte(raw), []Reading{{WindowKind: "weekly", Bucket: "seven_day", WindowMinutes: 10080, UsedPercent: 40, ResetsAt: week, ReadAt: now, Source: "harness"}}, now)
	if hit == nil || hit.Window != "" || len(hit.Readings) != 0 || hit.ResetsAt == nil || !hit.ResetsAt.Equal(stop) {
		t.Fatalf("unnamed Claude rejection rewrote a window: %+v", hit)
	}
}

func TestClaudeStatuslineProjection(t *testing.T) {
	now := time.Now().UTC()
	reset := now.Add(time.Hour).Unix()
	raw := fmt.Sprintf(`{"rate_limits":{"five_hour":{"used_percentage":0,"resets_at":%d},"seven_day":{"used_percentage":42,"resets_at":%d},"unverified":{"used_percentage":50,"resets_at":%d}},"workspace":{"current_dir":"fixture-private-path"},"token":"fixture-secret","email":"fixture@example.test"}`, reset, reset, reset)
	rs := ClaudeStatusline([]byte(raw), now)
	if len(rs) != 2 || rs[0].UsedPercent != 0 || rs[1].UsedPercent != 42 {
		t.Fatal("missing documented windows")
	}
	wire, _ := json.Marshal(rs)
	for _, private := range []string{"fixture", "email", "token", "current_dir"} {
		if strings.Contains(string(wire), private) {
			t.Fatal("private data escaped")
		}
	}
	for _, raw := range []string{`{}`, `{"rate_limits":{"five_hour":{"resets_at":1}}}`, fmt.Sprintf(`{"rate_limits":{"five_hour":{"used_percentage":101,"resets_at":%d}}}`, reset), strings.Repeat("x", (64<<10)+1)} {
		if len(ClaudeStatusline([]byte(raw), now)) != 0 {
			t.Fatal("invalid reading accepted")
		}
	}
}

func TestCodexLimitBucketScope(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name, model, raw string
		stop             bool
	}{
		{"other model", "model-a", `{"rateLimitsByLimitId":{"model-b":{"limitId":"model-b","rateLimitReachedType":"rate_limit_reached"}}}`, false},
		{"unknown model", "", `{"rateLimitsByLimitId":{"model-a":{"rateLimitReachedType":"rate_limit_reached"}}}`, false},
		{"exact model", "model-a", `{"rateLimitsByLimitId":{"model-a":{"limitId":"model-a","rateLimitReachedType":"rate_limit_reached"}}}`, true},
		{"default bucket", "model-a", `{"rateLimitsByLimitId":{"codex":{"rateLimitReachedType":"rate_limit_reached"}}}`, true},
		{"unrelated denial", "model-a", `{"rateLimitsByLimitId":{"model-b":{"ordinaryUsageAllowed":false}}}`, false},
		{"no prefix guessing", "model-a-mini", `{"rateLimitsByLimitId":{"model-a":{"rateLimitReachedType":"rate_limit_reached"}}}`, false},
		{"mismatched inner id", "model-a", `{"rateLimitsByLimitId":{"model-a":{"limitId":"model-b","rateLimitReachedType":"rate_limit_reached"}}}`, false},
		{"unrelated primary snapshot", "model-a", `{"rateLimits":{"limitId":"model-b","rateLimitReachedType":"rate_limit_reached"}}`, false},
		{"global denial", "model-a", `{"ordinaryUsageAllowed":false,"rateLimitsByLimitId":{"model-b":{"rateLimitReachedType":"rate_limit_reached"}}}`, true},
		{"mixed buckets", "model-a", `{"rateLimitsByLimitId":{"model-a":{"rateLimitReachedType":null},"model-b":{"rateLimitReachedType":"rate_limit_reached"}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hit := CodexLimit([]byte(tc.raw), nil, now, tc.model)
			if (hit != nil) != tc.stop {
				t.Fatalf("stop=%t, want %t", hit != nil, tc.stop)
			}
			if hit != nil && (len(hit.Readings) != 0 || hit.ResetsAt != nil || hit.Window != "") {
				t.Fatal("snapshot-level stop invented window bounds")
			}
		})
	}
}
