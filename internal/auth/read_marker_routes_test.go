// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http/httptest"
	"testing"
)

func TestReadMarkerHasNoAgentScope(t *testing.T) {
	path := "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/harness-sessions/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/read-marker"
	for _, method := range []string{"GET", "PUT"} {
		got, ok := coreAgentScope(httptest.NewRequest(method, path, nil))
		if !ok || got != "" {
			t.Fatalf("%s scope %q controlled=%v", method, got, ok)
		}
	}
}
