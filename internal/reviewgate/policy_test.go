// SPDX-License-Identifier: AGPL-3.0-only
package reviewgate

import (
	"strings"
	"testing"
)

// Risk: disabling a family restriction must not turn an unfinished, unverified,
// malformed or changes verdict into approval (authority and false done).
func TestFamilyPolicyOnlyRelaxesFamilyRequirement(t *testing.T) {
	profile, model := "profile", "review-model"
	for _, tc := range []struct {
		name, mode, reviewer, status, evidence, output, reason string
		allowed                                                []string
		want                                                   bool
	}{
		{name: "default same", mode: "other_family", reviewer: "openai", status: "completed", evidence: "vendor_reported", output: "VERDICT: ok", reason: "not cross-family"},
		{name: "default different", mode: "other_family", reviewer: "anthropic", status: "completed", evidence: "vendor_reported", output: "VERDICT: ok", reason: "author openai, reviewer anthropic", want: true},
		{name: "off same", mode: "off", reviewer: "openai", status: "completed", evidence: "vendor_reported", output: "VERDICT: ok", reason: "policy off", want: true},
		{name: "off running", mode: "off", reviewer: "openai", status: "running", evidence: "vendor_reported", output: "VERDICT: ok", reason: "gate stays closed"},
		{name: "off unverified", mode: "off", reviewer: "openai", status: "completed", evidence: "unverified", output: "VERDICT: ok", reason: "not verified"},
		{name: "off malformed", mode: "off", reviewer: "openai", status: "completed", evidence: "vendor_reported", output: "ok", reason: "final line"},
		{name: "off changes", mode: "off", reviewer: "openai", status: "completed", evidence: "vendor_reported", output: "FINDING: high a.go:1 Broken.\nVERDICT: changes", reason: "Changes are required"},
		{name: "allowlist outside", mode: "allowlist", allowed: []string{"google"}, reviewer: "anthropic", status: "completed", evidence: "vendor_reported", output: "VERDICT: ok", reason: "anthropic not allowed"},
		{name: "allowlist inside", mode: "allowlist", allowed: []string{"anthropic"}, reviewer: "anthropic", status: "completed", evidence: "vendor_reported", output: "VERDICT: ok", want: true},
		{name: "allowlist same", mode: "allowlist", allowed: []string{"openai"}, reviewer: "openai", status: "completed", evidence: "vendor_reported", output: "VERDICT: ok", reason: "not cross-family"},
		{name: "invalid mode", mode: "typo", reviewer: "anthropic", status: "completed", evidence: "vendor_reported", output: "VERDICT: ok", reason: "invalid"},
		{name: "unknown family off", mode: "off", reviewer: "unknown", status: "completed", evidence: "vendor_reported", output: "VERDICT: ok", reason: "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := Binding{AuthorFamily: "openai", ReviewerFamily: &tc.reviewer, ProfileID: &profile}
			ok, reason := Gate(tc.status, tc.evidence, &model, b, Parse(tc.output), FamilyPolicy{Mode: tc.mode, AllowedFamilies: tc.allowed})
			if ok != tc.want || !strings.Contains(reason, tc.reason) {
				t.Fatalf("gate = %v, %q", ok, reason)
			}
		})
	}
}
