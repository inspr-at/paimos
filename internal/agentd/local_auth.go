// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import "context"

// LocalAuthenticator is injected in tests only. Production uses LocalAuthentication
// inside agentd; no socket field, helper, command or environment can confirm it.
type LocalAuthenticator interface {
	Confirm(context.Context, string) error
}

type systemLocalAuthenticator struct{}

// expectedTeamID is the Apple Developer ID team that must have signed the
// running darwin daemon. Only the release build sets it, through
// -X github.com/inspr-at/paimos/internal/agentd.expectedTeamID=<team>
// (scripts/build-release-binaries.sh). Development and Nix builds leave it
// empty, so Mac confirmation fails closed there.
var expectedTeamID string

// localAuthTeamAllowed reports whether a valid hardened Developer ID signature
// by team actual satisfies the team this build expects. An empty or malformed
// expected team never matches.
func localAuthTeamAllowed(expected, actual string) bool {
	return validTeamID(expected) && actual == expected
}

// validTeamID accepts Apple's 10-character upper-case alphanumeric team ids.
func validTeamID(team string) bool {
	if len(team) != 10 {
		return false
	}
	for _, r := range team {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// localAuthUnsignedMessage explains why Mac confirmation is unavailable when
// the signature check fails.
func localAuthUnsignedMessage(expected string) string {
	if !validTeamID(expected) {
		return "local confirmation unavailable: this paimos-agentd build carries no Developer ID team (development or Nix build); install the signed release daemon"
	}
	return "local confirmation unavailable: installed paimos-agentd or aeon-agentd must have a valid hardened Developer ID signature by team " + expected + " without debugging or library-validation exceptions"
}
