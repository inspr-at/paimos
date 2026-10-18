// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"net/http/httptest"
	"testing"
)

func TestReviewPolicyAgentScopeIsExplicitAndBounded(t *testing.T) {
	project := "/api/projects/57000000-0000-4000-8000-000000000001/review-policy"
	settings := "/api/settings/review-policy"
	for _, tc := range []struct{ method, path, want string }{
		{"GET", settings, "reviewpolicy.read"},
		{"HEAD", settings, "reviewpolicy.read"},
		{"PUT", settings, "reviewpolicy.manage"},
		{"GET", project, "reviewpolicy.read"},
		{"HEAD", project, "reviewpolicy.read"},
		{"PUT", project, "reviewpolicy.manage"},
		{"DELETE", project, "reviewpolicy.manage"},
	} {
		if got, ok := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil)); !ok || got != tc.want {
			t.Errorf("%s %s: %q %v, want %s", tc.method, tc.path, got, ok, tc.want)
		}
	}
	for _, tc := range []struct{ method, path string }{
		{"POST", settings}, {"DELETE", settings}, {"PATCH", settings},
		{"GET", "/api/settings"}, {"PUT", "/api/settings"}, {"GET", "/api/settings/other"},
		{"PUT", settings + "/extra"}, {"POST", project}, {"PATCH", project},
		{"GET", project + "/extra"}, {"DELETE", "/api/projects/not-a-uuid/review-policy"},
		{"GET", "/api/projects/review-policy"}, {"PUT", "/api/projects/57000000-0000-4000-8000-000000000001/settings"},
	} {
		if got, ok := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil)); ok || got != "" {
			t.Errorf("unexpected agent route %s %s: %q %v", tc.method, tc.path, got, ok)
		}
	}
}
