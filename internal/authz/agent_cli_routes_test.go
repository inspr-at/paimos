// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"slices"
	"strings"
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
		"GET /api/model-preferences",
		"GET /api/work-kinds",
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
		// Route alternatives are OR, as in RequirePattern. A declaration such
		// as models.read|nodes.read is not itself a grantable key scope.
		reachable := slices.ContainsFunc(strings.Split(want, "|"), func(permission string) bool {
			return slices.Contains(have, permission) || CoordinatorCeiling(have, permission)
		})
		if !reachable {
			t.Errorf("%s needs %s, which the coordinator key set lacks", pattern, want)
		}
	}
	for _, forbidden := range []string{"rules.write", "rules.publish", "models.manage", "model_prefs.manage"} {
		if slices.Contains(have, forbidden) {
			t.Errorf("coordinator ceiling includes %s", forbidden)
		}
	}
}
