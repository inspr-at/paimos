// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"runtime"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
)

func TestVerificationAdvertisementMatchesLocalAdapters(t *testing.T) {
	capabilities := verificationCapabilities(runtime.GOOS, runtime.GOARCH)
	for _, adapter := range []agentd.Adapter{
		agentd.NewCodexAdapter("/unused", nil), agentd.NewCursorAdapter("/unused", nil),
		agentd.NewClaudeAdapter("/unused", "/unused", "/unused", nil), agentd.NewGrokAdapter(),
	} {
		capability := capabilities[adapter.Name()]
		local, ok := adapter.(agentd.VerificationAdapter)
		if !ok || local.VerificationSupported() != capability.Supported {
			t.Fatalf("%s server and daemon qualification differ", adapter.Name())
		}
	}
}

func TestUnqualifiedVerificationNeverAdvertisedAsDispatchTarget(t *testing.T) {
	for _, platform := range []string{"", "darwin", "linux"} {
		for _, arch := range []string{"", "arm64", "amd64"} {
			caps := verificationCapabilities(platform, arch)
			for _, harness := range []string{"codex", "cursor"} {
				c := caps[harness]
				if c.Supported || c.Policy != "unavailable" || c.Reason == "" || strings.ContainsAny(c.Reason, "\r\n") {
					t.Fatalf("%s/%s/%s lacks a truthful one-line refusal", platform, arch, harness)
				}
			}
		}
	}
	for _, target := range VerificationTargets() {
		if strings.HasSuffix(target, "/codex") || strings.HasSuffix(target, "/cursor") {
			t.Fatalf("unqualified dispatch target: %s", target)
		}
	}
}
