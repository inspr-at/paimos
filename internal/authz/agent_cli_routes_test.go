// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"slices"
	"testing"
)

// CoordinatorKeyScopes is the CLI coordinator ceiling. Keys minted before
// AEON-327 carry only CoordinatorBaseScopes; IsCoordinatorKey still recognizes
// them. The aeon/paimos CLI reads kinds and projects before most commands, so
// this set must reach heartbeat, inbox, model resolve and rules reads. It does
// not include rules writes or model administration.
func TestCoordinatorPermissionsCoverCLIHeartbeatPath(t *testing.T) {
	have := CoordinatorKeyScopes
	for _, pattern := range []string{
		"GET /api/kinds",
		"GET /api/projects",
		"POST /api/projects/{projectId}/harness-sessions/{sessionId}/heartbeat",
		"GET /api/models",
		"GET /api/models/resolve",
		"GET /api/rules/layers",
		"GET /api/rules/sets",
		"GET /api/rules/merged",
	} {
		want, ok := PermissionForPattern(pattern)
		if !ok {
			t.Fatalf("%s has no declared permission", pattern)
		}
		if !slices.Contains(have, want) && !CoordinatorCeiling(have, want) {
			t.Errorf("%s needs %s, which the coordinator key set lacks", pattern, want)
		}
	}
	for _, forbidden := range []string{"rules.write", "rules.publish", "models.manage"} {
		if slices.Contains(have, forbidden) {
			t.Errorf("coordinator ceiling includes %s", forbidden)
		}
	}
}
