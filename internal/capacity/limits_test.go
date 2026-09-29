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
		{"codex", `{"method":"account/rateLimits/updated","params":{"ordinaryUsageAllowed":false}}`, true},
		{"codex", `{"method":"turn/failed","params":{"error":{"codexErrorInfo":{"rateLimitReachedType":"usage"}}}}`, true},
		{"claude", fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":%d}}`, reset.Unix()), true},
		{"grok", fmt.Sprintf(`{"error":{"code":"usage_limit_reached","data":{"window_minutes":300,"resets_at":%d}}}`, reset.Unix()), true},
		{"grok", `{"error":{"code":"rate_limited"}}`, false},
		{"grok", `{"error":{"data":{"type":"usage_pool_exhausted"}}}`, false},
		{"cursor", fmt.Sprintf(`{"error":{"code":429,"data":{"window_minutes":300,"resets_at":%d}}}`, reset.Unix()), true},
		{"cursor", `{"error":{"code":-32000,"data":{"code":"quota_exceeded"}}}`, false},
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
	for _, raw := range []string{`{"message":"rate_limited"}`, `{"error":{"message":"usage_limit_reached"}}`, `{"error":{"code":"auth_failed"}}`, `{"ordinaryUsageAllowed":true}`} {
		for _, vendor := range []string{"codex", "claude", "grok", "cursor", "pi"} {
			if VendorLimit(vendor, []byte(raw), nil, now) != nil {
				t.Fatal("ordinary error classified as quota")
			}
		}
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
