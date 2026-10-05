// SPDX-License-Identifier: AGPL-3.0-only

package authz

import "testing"

func TestExitConfirmationRoutePermission(t *testing.T) {
	pattern := "POST /api/projects/{projectId}/harness-sessions/{sessionId}/confirm-exit"
	if got, ok := PermissionForPattern(pattern); !ok || got != "harness.worker" {
		t.Fatalf("exit confirmation permission %q, declared=%v; want harness.worker", got, ok)
	}
}
