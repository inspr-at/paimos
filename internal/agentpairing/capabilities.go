// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

// VerificationCapability describes the trusted helper shipped with this server
// release. Local capability claims cannot widen it. Platform/service readiness
// remains separately qualified and the helper must still enforce its controls.
type VerificationCapability struct {
	Supported bool   `json:"supported"`
	Policy    string `json:"policy"`
	Reason    string `json:"reason"`
}

// Empty platform/arch is the guide's conservative global capability view.
// Native Grok's two qualified variants are pinned again by the local runtime.
func verificationCapabilities(platform, arch string) map[string]VerificationCapability {
	grok := VerificationCapability{false, "unavailable", "Native Grok verification is qualified only on macOS arm64 with a pinned native build."}
	if platform == "darwin" && arch == "arm64" {
		grok = VerificationCapability{true, "no_tools", ""}
	}
	return map[string]VerificationCapability{
		"claude": {true, "no_tools", ""},
		"codex":  {false, "unavailable", "Codex verification cannot yet guarantee external/MCP isolation."},
		"cursor": {false, "unavailable", "Cursor external/MCP isolation is awaiting qualification."},
		"grok":   grok,
	}
}

// VerificationTargets binds dispatch to immutable request OS/architecture and
// the same server-owned qualification used by review/approval. This is the seam
// for a later independently qualified platform-specific helper implementation.
func VerificationTargets() []string {
	var supported []string
	for _, platform := range []string{"darwin", "linux"} {
		for _, arch := range []string{"arm64", "amd64"} {
			for _, h := range []string{"claude", "codex", "cursor", "grok"} {
				if verificationCapabilities(platform, arch)[h].Supported {
					supported = append(supported, platform+"/"+arch+"/"+h)
				}
			}
		}
	}
	return supported
}
