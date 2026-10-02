// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"net/http/httptest"
	"testing"
)

func TestAutopilotAgentRouteCeiling(t *testing.T) {
	lane := "/api/autopilot-lanes/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	project := "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/autopilot-lanes"
	for _, tc := range []struct{ method, path, want string }{
		{"GET", project, "autopilot.read"}, {"HEAD", project, "autopilot.read"},
		{"GET", lane, "autopilot.read"}, {"GET", lane + "/preview", "autopilot.read"},
		{"GET", lane + "/history", "autopilot.read"}, {"POST", lane + "/pause", "autopilot.pause"},
		{"POST", project, ""}, {"PATCH", lane, ""}, {"POST", lane + "/resume", ""}, {"POST", lane + "/prepare", ""},
		{"GET", lane + "/extra", ""}, {"GET", lane + "/preview/extra", ""},
		{"GET", "/api/autopilot-lanes/not-a-uuid", ""}, {"GET", "/api/projects/not-a-uuid/autopilot-lanes", ""},
	} {
		got, ok := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil))
		if got != tc.want || ok != (tc.want != "") {
			t.Fatalf("%s %s got %q controlled=%v", tc.method, tc.path, got, ok)
		}
	}
	if _, err := NormalizeScopes([]string{"autopilot.read", "autopilot.pause"}); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeScopes([]string{"autopilot.manage"}); err == nil {
		t.Fatal("person governance issued to an agent")
	}
}
