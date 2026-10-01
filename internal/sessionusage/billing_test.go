// SPDX-License-Identifier: AGPL-3.0-only
package sessionusage

import "testing"

func TestCountReportExplicitBilling(t *testing.T) {
	for _, mode := range []string{"api", "subscription", "unknown", "ChatGPT Plus", "API", ""} {
		report, ok := CountReport("model-a", 100, 20, 30, true, mode)
		want := "unknown"
		if mode == "api" || mode == "subscription" {
			want = mode
		}
		if !ok || report.BillingMode != want {
			t.Fatalf("%q: %+v %v", mode, report, ok)
		}
		parser, err := NewManagedCodex("thread-a", "model-a", mode)
		if err != nil || parser.billingMode != want {
			t.Fatalf("managed %q: %v", mode, err)
		}
	}
}
