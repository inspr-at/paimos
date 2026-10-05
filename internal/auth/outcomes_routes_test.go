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
