// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"net/http/httptest"
	"testing"
)

func TestCapacityReadingUsesProbeScope(t *testing.T) {
	id := "/api/agent-accounts/11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct{ method, path, scope string }{
		{"GET", id + "/readings", "account.probe"},
		{"POST", id + "/readings", "account.probe"},
		{"PUT", id + "/signals", "account.probe"},
		{"GET", id + "/statusline", "account.probe"},
		{"POST", id + "/quota-key", "account.probe"},
	} {
		scope, ok := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil))
		if !ok || scope != tc.scope {
			t.Fatalf("%s %s: %s %v", tc.method, tc.path, scope, ok)
		}
	}
	if scope, ok := coreAgentScope(httptest.NewRequest("PUT", id+"/statusline", nil)); ok || scope != "" {
		t.Fatal("agent statusline opt-in")
	}
}

func TestAccountLinkScopeAllowsOnlyPairedDeviceOperation(t *testing.T) {
	path := "/api/agent-pairing/account-link"
	if scope, ok := coreAgentScope(httptest.NewRequest("POST", path, nil)); !ok || scope != "account.probe" {
		t.Fatal("paired device operation has no probe scope")
	}
	for _, route := range []struct{ method, path string }{
		{"GET", path}, {"POST", path + "/lookup"}, {"POST", path + "/11111111-1111-4111-8111-111111111111/approve"},
		{"GET", "/api/agent-pairing/account-links"}, {"POST", "/api/agent-pairing/account-links/11111111-1111-4111-8111-111111111111/unlink"},
	} {
		if scope, ok := coreAgentScope(httptest.NewRequest(route.method, route.path, nil)); ok || scope != "" {
			t.Fatal("person ownership action exposed to an agent key")
		}
	}
}
