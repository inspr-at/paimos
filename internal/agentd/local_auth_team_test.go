// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"strings"
	"testing"
)

func TestLocalAuthTeamAllowed(t *testing.T) {
	for _, tc := range []struct {
		expected, actual string
		want             bool
	}{
		{"P66J39QV6V", "P66J39QV6V", true},
		{"P66J39QV6V", "ZZZZZZZZZZ", false},
		{"P66J39QV6V", "", false},
		{"P66J39QV6V", "p66j39qv6v", false},
		{"", "", false},
		{"", "P66J39QV6V", false},
		{"p66j39qv6v", "p66j39qv6v", false},
		{"P66J39QV6", "P66J39QV6", false},
		{"P66J39QV6V ", "P66J39QV6V ", false},
	} {
		if got := localAuthTeamAllowed(tc.expected, tc.actual); got != tc.want {
			t.Errorf("localAuthTeamAllowed(%q, %q) = %v, want %v", tc.expected, tc.actual, got, tc.want)
		}
	}
}

func TestTestBinaryHasNoExpectedTeam(t *testing.T) {
	// Only the release build injects a team; any other build must fail closed.
	if expectedTeamID != "" {
		t.Fatalf("expectedTeamID %q set outside a release build", expectedTeamID)
	}
	if !strings.Contains(localAuthUnsignedMessage(""), "no Developer ID team") {
		t.Fatal("missing-team message does not say the build has no team")
	}
	if !strings.Contains(localAuthUnsignedMessage("P66J39QV6V"), "team P66J39QV6V") {
		t.Fatal("unsigned message does not name the expected team")
	}
}
