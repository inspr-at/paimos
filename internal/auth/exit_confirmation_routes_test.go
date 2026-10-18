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

func TestAgentRecoveryRuntimeRouteCeiling(t *testing.T) {
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	for _, path := range []string{"/api/harness-recoveries/claim", "/api/harness-recoveries/" + id + "/complete"} {
		if got, ok := coreAgentScope(httptest.NewRequest("POST", path, nil)); !ok || got != "harness.worker" {
			t.Fatalf("scope %q declared=%v", got, ok)
		}
		if got, ok := coreAgentScope(httptest.NewRequest("GET", path, nil)); ok || got != "" {
			t.Fatal("recovery runtime GET unexpectedly allowed")
		}
	}
	for _, path := range []string{"/api/harness-recoveries", "/api/harness-recoveries/not-an-id/complete", "/api/harness-recoveries/" + id + "/secrets", "/api/harness-recoveries/claim/extra"} {
		if _, ok := coreAgentScope(httptest.NewRequest("POST", path, nil)); ok {
			t.Fatal("unknown runtime recovery operation allowed")
		}
	}
}
