// SPDX-License-Identifier: AGPL-3.0-only

package agentcompat

import "testing"

func TestSupportedWindow(t *testing.T) {
	p := Supported()
	for _, tc := range []struct {
		name, protocol, version, scheme, status string
	}{
		{"floor", Protocol, MinimumVersion, p.VersionScheme, "compatible"},
		{"newer agent on old server", Protocol, "261002072608.0.0", p.VersionScheme, "compatible"},
		{"below minimum", Protocol, "260930072608.0.0", p.VersionScheme, "update_required"},
		{"protocol mismatch", "pairing-v2", MinimumVersion, p.VersionScheme, "protocol_mismatch"},
		{"legacy unknown", "", "", "", "unknown"},
		{"development", Protocol, "dev", p.VersionScheme, "unknown"},
		{"absent scheme", Protocol, MinimumVersion, "", "unknown"},
		{"other era", Protocol, MinimumVersion, "inspr-calendar-v2", "unknown"},
		{"invalid calendar", Protocol, "261032072608.0.0", p.VersionScheme, "unknown"},
		{"noncanonical", Protocol, "261002072608.0.0+build", p.VersionScheme, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := p.Check(Release{Protocol: tc.protocol, Version: tc.version, VersionScheme: tc.scheme})
			if got.Status != tc.status {
				t.Fatalf("got %s, want %s", got.Status, tc.status)
			}
			needsUpdate := tc.status == "update_required" || tc.status == "protocol_mismatch"
			if needsUpdate != (got.Action != "") {
				t.Fatalf("unexpected advice: %+v", got)
			}
		})
	}
}
