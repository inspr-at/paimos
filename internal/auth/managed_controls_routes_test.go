// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"net/http/httptest"
	"testing"
)

func TestManagedContextWorkerRouteCeiling(t *testing.T) {
	path := "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/harness-sessions/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/managed-context"
	if got, ok := coreAgentScope(httptest.NewRequest("POST", path, nil)); !ok || got != "harness.worker" {
		t.Fatalf("%q %v", got, ok)
	}
}

func TestManagedControlRouteRequiresControlScope(t *testing.T) {
	for _, path := range []string{
		"/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/harness-sessions/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/managed-controls",
		"/api/harness-sessions/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/managed-controls",
	} {
		if got, ok := coreAgentScope(httptest.NewRequest("POST", path, nil)); !ok || got != "harness.control" {
			t.Fatalf("%s: %q %v", path, got, ok)
		}
	}
}

func TestManagedSettingsRouteRequiresControlScope(t *testing.T) {
	path := "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/harness-sessions/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/managed-settings"
	if got, ok := coreAgentScope(httptest.NewRequest("GET", path, nil)); !ok || got != "harness.control" {
		t.Fatalf("%q %v", got, ok)
	}
}
