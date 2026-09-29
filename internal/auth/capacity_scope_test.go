// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"net/http/httptest"
	"testing"
)

func TestCapacityReadingUsesProbeScope(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		scope, ok := coreAgentScope(httptest.NewRequest(method, "/api/agent-accounts/11111111-1111-4111-8111-111111111111/readings", nil))
		if !ok || scope != "account.probe" {
			t.Fatalf("%s: %s %v", method, scope, ok)
		}
	}
}
