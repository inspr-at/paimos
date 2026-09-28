// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http/httptest"
	"testing"
)

func TestRulesReceiptAgentRouteCeiling(t *testing.T) {
	path := "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/harness-sessions/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/rules-receipts"
	for method, permission := range map[string]string{"GET": "harness.read", "HEAD": "harness.read", "POST": "harness.worker"} {
		if got, ok := coreAgentScope(httptest.NewRequest(method, path, nil)); !ok || got != permission {
			t.Fatalf("%s permission %q, declared=%v", method, got, ok)
		}
	}
}
