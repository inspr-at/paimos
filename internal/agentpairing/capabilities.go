// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import "github.com/inspr-at/paimos/internal/agentverification"

// VerificationCapability describes the trusted helper shipped with this server
// release. Local capability claims cannot widen it. Platform/service readiness
// remains separately qualified and the helper must still enforce its controls.
type VerificationCapability = agentverification.Capability

// Empty platform/arch is the guide's conservative global capability view.
// Native Grok's two qualified variants are pinned again by the local runtime.
func verificationCapabilities(platform, arch string) map[string]VerificationCapability {
	capabilities := make(map[string]VerificationCapability, 5)
	for _, harness := range []string{"claude", "codex", "cursor", "grok", "pi"} {
		capabilities[harness] = agentverification.For(harness, platform, arch)
	}
	return capabilities
}

// VerificationTargets binds dispatch to immutable request OS/architecture and
// the same server-owned qualification used by review/approval. This is the seam
// for a later independently qualified platform-specific helper implementation.
func VerificationTargets() []string {
	var supported []string
	for _, platform := range []string{"darwin", "linux"} {
		for _, arch := range []string{"arm64", "amd64"} {
			for _, h := range []string{"claude", "codex", "cursor", "grok", "pi"} {
				if verificationCapabilities(platform, arch)[h].Supported {
					supported = append(supported, platform+"/"+arch+"/"+h)
				}
			}
		}
	}
	return supported
}
