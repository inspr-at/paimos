// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http/httptest"
	"testing"
)

func TestExitConfirmationAgentRouteCeiling(t *testing.T) {
	path := "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/harness-sessions/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/confirm-exit"
	if got, ok := coreAgentScope(httptest.NewRequest("POST", path, nil)); !ok || got != "harness.worker" {
		t.Fatalf("exit confirmation scope %q, declared=%v; want harness.worker", got, ok)
	}
}
