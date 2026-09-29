// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"net/http/httptest"
	"testing"
)

func TestRulesAgentRouteCeiling(t *testing.T) {
	for _, tc := range []struct{ method, path, want string }{
		{"GET", "/api/rules/merged", "rules.read"}, {"GET", "/api/rules/comparisons", "rules.read"},
		{"POST", "/api/rules/comparisons", "rules.write"}, {"HEAD", "/api/rules/sets/id/versions/v", "rules.read"},
		{"POST", "/api/rules/sets", "rules.write"}, {"PUT", "/api/rules/sets/id/draft", "rules.write"},
		{"POST", "/api/rules/sets/id/publish", ""}, {"POST", "/api/rules/sets/id/restore", ""}, {"POST", "/api/rules/anything", ""},
	} {
		got, ok := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil))
		if got != tc.want || ok != (tc.want != "") {
			t.Fatalf("%s %s: %s %v", tc.method, tc.path, got, ok)
		}
	}
	if _, err := NormalizeScopes([]string{"rules.publish"}); err == nil {
		t.Fatal("publish granted to agent key")
	}
	if _, err := NormalizeScopes([]string{"rules.read", "rules.write"}); err != nil {
		t.Fatal(err)
	}
}
