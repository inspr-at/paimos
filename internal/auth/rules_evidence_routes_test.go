// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"net/http/httptest"
	"testing"
)

// Risk: a new delivery route could accidentally inherit harness.write or the
// bare computer metadata exception instead of exact run content authority.
func TestRulesDeliveryAgentRouteCeiling(t *testing.T) {
	base := "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/harness-sessions/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/"
	for _, test := range []struct{ method, suffix, permission string }{
		{"POST", "rules-delivery-manifest", "harness.worker"},
		{"POST", "rules-delivery-evidence", "harness.worker"},
		{"GET", "rules-delivery-evidence", "harness.read"},
		{"HEAD", "rules-delivery-evidence", "harness.read"},
	} {
		r := httptest.NewRequest(test.method, base+test.suffix, nil)
		r.Pattern = test.method + " /api/projects/{projectId}/harness-sessions/{sessionId}/" + test.suffix
		if scope, ok := coreAgentScope(r); !ok || scope != test.permission {
			t.Fatalf("%s %s scope %q declared=%v", test.method, test.suffix, scope, ok)
		}
		if transportMetadataRoute(r) || transportAttachedHookRoute(r) {
			t.Fatal("delivery route accepted bare transport authority")
		}
	}
}
