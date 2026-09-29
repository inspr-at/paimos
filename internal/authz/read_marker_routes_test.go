// SPDX-License-Identifier: AGPL-3.0-only

package authz

import "testing"

func TestReadMarkerRouteIsPersonSessionRead(t *testing.T) {
	for _, method := range []string{"GET", "PUT"} {
		pattern := method + " /api/projects/{projectId}/harness-sessions/{sessionId}/read-marker"
		got, ok := PermissionForPattern(pattern)
		if !ok || got != "harness.read" {
			t.Fatalf("%s permission %q ok=%v", method, got, ok)
		}
	}
}
