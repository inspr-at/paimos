// SPDX-License-Identifier: AGPL-3.0-only

package agentpairing_test

import (
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/hookcap"
)

func TestHookCapabilityReconcileProjectsConservatively(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude", "codex")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	request := map[string]any{"tenant_id": f.tenantID, "request_id": p.id, "lifecycle_secret": p.lifecycle, "hook_capabilities": []hookcap.Capability{
		{Harness: "claude", Version: "2.fixture", OS: "darwin", Verified: true, Blocker: ""},
		{Harness: "codex", Version: "1.fixture", OS: "darwin", Blocker: "project_override"},
	}}
	var result agentpairing.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/reconcile", request, false, "", 200), &result)
	if len(result.HookCapabilities) != 2 || result.HookCapabilities[0].Verified || result.HookCapabilities[0].Blocker != "qualification_pending" || result.HookCapabilities[1].Blocker != "project_override" {
		t.Fatal("client promoted readiness", result.HookCapabilities)
	}
	decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200), &result)
	if len(result.HookCapabilities) != 2 {
		t.Fatal("projection lost capability")
	}
	// Ordinary legacy reconciliation omits the additive field and preserves it.
	delete(request, "hook_capabilities")
	decodeResult(t, f.call("POST", "/api/agent-pairing/reconcile", request, false, "", 200), &result)
	if len(result.HookCapabilities) != 2 {
		t.Fatal("legacy client cleared evidence")
	}
	for _, caps := range [][]hookcap.Capability{
		{{Harness: "claude", OS: "darwin", Blocker: "fixture-private-data"}},
		{{Harness: "claude", OS: "darwin", Verified: true, Blocker: "feature_disabled"}},
		{{Harness: "claude", OS: "linux", Blocker: "feature_disabled"}},
		{{Harness: "pi", OS: "darwin", Blocker: "feature_disabled"}},
		{{Harness: "claude", OS: "darwin", Blocker: "feature_disabled"}, {Harness: "claude", OS: "darwin", Blocker: "feature_disabled"}},
	} {
		request["hook_capabilities"] = caps
		f.call("POST", "/api/agent-pairing/reconcile", request, false, "", 400)
	}
	request["hook_capabilities"] = []hookcap.Capability{}
	request["lifecycle_secret"] = nonce()
	f.call("POST", "/api/agent-pairing/reconcile", request, false, "", 404)
}
