// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: a bare paired credential is told to upgrade for a person session
// control, so the pairing ceiling never refuses it, or a content credential
// can select that control.
func TestBareTransportPersonControlKeepsPairingCeiling(t *testing.T) {
	const (
		project = "11111111-1111-4111-8111-111111111111"
		session = "22222222-2222-4222-8222-222222222222"
		order   = "33333333-3333-4333-8333-333333333333"
	)
	computer := tenant.Principal{TransportComputerID: "computer", RunProjectID: project, RunID: "44444444-4444-4444-8444-444444444444", RunWorkOrderID: order}
	boundary := &Module{}
	request := func(method, pattern, path string) *http.Request {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.Pattern = pattern
		return r
	}
	upgrade := func(r *http.Request) bool {
		t.Helper()
		_, _, err := boundary.runContentBoundary(r, computer)
		var denied *agentpairing.Error
		return errors.As(err, &denied) && denied.Status == http.StatusForbidden && denied.Code == "run_capability_upgrade_required"
	}
	root := "/api/projects/" + project + "/harness-sessions/" + session
	for _, route := range []struct{ pattern, path string }{
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/controls/interrupt", root + "/controls/interrupt"},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/controls/stop", root + "/controls/stop"},
	} {
		r := request(http.MethodPost, route.pattern, route.path)
		if upgrade(r) {
			t.Fatalf("%s demanded a content capability before the pairing ceiling", route.pattern)
		}
		if _, _, err := boundary.runContentBoundary(r, computer); err != nil {
			t.Fatalf("%s consumed the pairing ceiling: %v", route.pattern, err)
		}
		if runCapabilityTarget(r, computer) {
			t.Fatalf("content credential selected %s", route.pattern)
		}
	}
	node := request(http.MethodGet, "GET /api/nodes/{nodeId}", "/api/nodes/"+order)
	if !upgrade(node) {
		t.Fatal("node content no longer requires a capability")
	}
	heartbeat := request(http.MethodPost, "POST /api/projects/{projectId}/harness-sessions/{sessionId}/heartbeat", root+"/heartbeat")
	if !upgrade(heartbeat) {
		t.Fatal("session heartbeat no longer requires a capability")
	}
	if !runCapabilityTarget(heartbeat, computer) {
		t.Fatal("bound session heartbeat lost its content target")
	}
	if upgrade(request(http.MethodGet, "GET /api/me", "/api/me")) {
		t.Fatal("metadata identity demanded a content capability")
	}
}
