// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"net/http/httptest"
	"testing"
)

func TestRecurrenceAgentScopeIsExplicitAndBounded(t *testing.T) {
	base := "/api/recurrences"
	id := base + "/57000000-0000-4000-8000-000000000001"
	for _, tc := range []struct{ method, path string }{
		{"GET", base}, {"POST", base}, {"POST", base + "/preview"},
		{"GET", id}, {"PUT", id}, {"DELETE", id}, {"HEAD", id + "/history"},
		{"GET", id + "/preview"}, {"GET", id + "/history"}, {"GET", id + "/releases"}, {"GET", id + "/guardrails"},
		{"POST", id + "/pause"}, {"POST", id + "/resume"}, {"POST", id + "/run-now"},
	} {
		if got, ok := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil)); !ok || got != "recurrences.manage" {
			t.Errorf("%s %s: %q %v", tc.method, tc.path, got, ok)
		}
	}
	for _, tc := range []struct{ method, path string }{
		{"PUT", id + "/guardrails"}, {"DELETE", base}, {"PATCH", id}, {"POST", id + "/history"},
		{"GET", id + "/secrets"}, {"POST", id + "/unknown"}, {"GET", id + "/history/private"},
	} {
		if got, ok := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil)); ok || got != "" {
			t.Errorf("unexpected agent route %s %s: %q %v", tc.method, tc.path, got, ok)
		}
	}
}
