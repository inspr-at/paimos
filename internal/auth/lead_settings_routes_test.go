// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"net/http/httptest"
	"testing"
)

func TestLeadSettingsAgentRoutesReadOnly(t *testing.T) {
	project := "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/lead-settings"
	for _, tc := range []struct{ method, path, scope string }{
		{"GET", project, "nodes.read"}, {"HEAD", project, "nodes.read"},
		{"PUT", project, ""}, {"DELETE", project, ""}, {"POST", project, ""},
		{"GET", project + "/extra", ""}, {"GET", "/api/projects/bad/lead-settings", ""},
		{"GET", "/api/settings/lead-policy", ""}, {"PUT", "/api/settings/lead-policy", ""},
	} {
		scope, allowed := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil))
		if scope != tc.scope || allowed != (tc.scope != "") {
			t.Fatalf("%s %s: scope %q allowed %v", tc.method, tc.path, scope, allowed)
		}
	}
}
