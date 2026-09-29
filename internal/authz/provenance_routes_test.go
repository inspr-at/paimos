// SPDX-License-Identifier: AGPL-3.0-only

package authz

import "testing"

// Production routing fails closed unless RoutePermissions names the pattern.
// Instruction provenance is its own route: GET is harness.read, POST is
// harness.worker. It is not part of registration, heartbeat, or usage.
func TestInstructionProvenanceRoutePermissions(t *testing.T) {
	get := "GET /api/projects/{projectId}/harness-sessions/{sessionId}/provenance"
	post := "POST /api/projects/{projectId}/harness-sessions/{sessionId}/provenance"
	if got, ok := PermissionForPattern(get); !ok || got != "harness.read" {
		t.Fatalf("GET permission %q ok=%v", got, ok)
	}
	if got, ok := PermissionForPattern(post); !ok || got != "harness.worker" {
		t.Fatalf("POST permission %q ok=%v", got, ok)
	}
	query := "GET /api/projects/{projectId}/instruction-provenance"
	if got, ok := PermissionForPattern(query); !ok || got != "harness.read" {
		t.Fatalf("query permission %q ok=%v", got, ok)
	}
	for _, other := range []string{
		"POST /api/projects/{projectId}/harness-sessions",
		"POST /api/projects/{projectId}/harness-sessions/{sessionId}/heartbeat",
		"POST /api/projects/{projectId}/harness-sessions/{sessionId}/usage",
	} {
		if got, _ := PermissionForPattern(other); got == "" {
			t.Fatalf("%s missing", other)
		}
	}
}
