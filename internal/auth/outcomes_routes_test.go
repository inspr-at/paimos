// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOutcomeAgentRouteCeiling(t *testing.T) {
	for _, tc := range []struct {
		method, path, want string
	}{
		{http.MethodGet, "/api/outcomes", "outcome.read"},
		{http.MethodHead, "/api/outcomes", "outcome.read"},
		{http.MethodPost, "/api/outcomes", "outcome.write"},
		{http.MethodGet, "/api/outcomes/measurement", "outcome.read"},
		{http.MethodHead, "/api/outcomes/measurement", "outcome.read"},
		{http.MethodPost, "/api/outcomes/measurement", ""},
		{http.MethodDelete, "/api/outcomes/measurement", ""},
		{http.MethodGet, "/api/outcomes/measurement/extra", ""},
		{http.MethodDelete, "/api/outcomes", ""},
		{http.MethodPost, "/api/outcomes/extra", ""},
	} {
		got, ok := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil))
		if got != tc.want || ok != (tc.want != "") {
			t.Fatalf("%s %s: got %q controlled %v", tc.method, tc.path, got, ok)
		}
	}
	if _, err := NormalizeScopes([]string{"outcome.read", "outcome.write"}); err != nil {
		t.Fatal(err)
	}
}

func TestLeadUsageAgentRouteCeiling(t *testing.T) {
	for _, tc := range []struct{ method, path, want string }{
		{http.MethodGet, "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/lead/usage", "harness.read"},
		{http.MethodHead, "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/lead/usage", "harness.read"},
		{http.MethodPost, "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/lead/usage", ""},
		{http.MethodGet, "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/lead/usage/extra", ""},
	} {
		got, ok := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil))
		if got != tc.want || ok != (tc.want != "") {
			t.Fatalf("%s %s: %q %v", tc.method, tc.path, got, ok)
		}
	}
}
