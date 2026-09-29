// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

func TestRulesAgentRouteCeiling(t *testing.T) {
	for _, tc := range []struct{ method, path, want string }{
		{"GET", "/api/rules/merged", "rules.read"}, {"GET", "/api/rules/channels", "rules.read"}, {"HEAD", "/api/rules/channels", "rules.read"}, {"POST", "/api/rules/channels", ""}, {"GET", "/api/rules/comparisons", "rules.read"},
		{"GET", "/api/rules/doctrine", "rules.read"}, {"HEAD", "/api/rules/doctrine", "rules.read"},
		{"POST", "/api/rules/doctrine/sources", ""}, {"PUT", "/api/rules/doctrine/sources/id", ""},
		{"DELETE", "/api/rules/doctrine/sources/id", ""}, {"POST", "/api/rules/doctrine/sources/id/index", ""},
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

func TestDoctrineRulesReadKeyAtMiddleware(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "doctrine-scope", "Doctrine scope")
	m := newMod(t, Config{})
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	reader, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tid}, "doctrine-reader", "", []string{"rules.read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tid}, "doctrine-writer", "", []string{"rules.write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, token string
		want                int
	}{
		{"GET", "/api/rules/doctrine", reader.Token, http.StatusNoContent},
		{"GET", "/api/rules/channels", reader.Token, http.StatusNoContent},
		{"HEAD", "/api/rules/channels", reader.Token, http.StatusNoContent},
		{"GET", "/api/rules/channels", writer.Token, http.StatusForbidden},
		{"POST", "/api/rules/channels", reader.Token, http.StatusForbidden},
		{"HEAD", "/api/rules/doctrine", reader.Token, http.StatusNoContent},
		{"GET", "/api/rules/doctrine", writer.Token, http.StatusForbidden},
		{"POST", "/api/rules/doctrine/sources", reader.Token, http.StatusForbidden},
		{"PUT", "/api/rules/doctrine/sources/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", writer.Token, http.StatusForbidden},
		{"DELETE", "/api/rules/doctrine/sources/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", reader.Token, http.StatusForbidden},
		{"POST", "/api/rules/doctrine/sources/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/index", writer.Token, http.StatusForbidden},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		setPolicyPattern(req)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != tc.want {
			t.Errorf("%s %s: status %d, want %d", tc.method, tc.path, res.Code, tc.want)
		}
	}
}
