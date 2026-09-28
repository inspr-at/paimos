// SPDX-License-Identifier: AGPL-3.0-only

// Package agentverification is the release-owned qualification shared by the
// pairing server and local adapters. A local claim or a prompt cannot widen it.
package agentverification

type Capability struct {
	Supported bool   `json:"supported"`
	Policy    string `json:"policy"`
	Reason    string `json:"reason"`
}

// For returns qualification, not vendor feature discovery. Changing a false
// result requires pre-start enforcement evidence, including hooks and inherited
// tools, while retaining the approved vendor sign-in. Fake protocol success
// alone cannot qualify the real harness. See AEON-238 / Knowledge MEM-1.
func For(harness, platform, arch string) Capability {
	switch harness {
	case "claude":
		return Capability{true, "no_tools", ""}
	case "codex":
		return Capability{false, "unavailable", "Codex read-only sandboxing does not isolate inherited MCP tools and startup hooks."}
	case "cursor":
		return Capability{false, "unavailable", "Cursor ask mode and an isolated config do not enforce a no-tools policy."}
	case "grok":
		if platform == "darwin" && arch == "arm64" {
			return Capability{true, "no_tools", ""}
		}
		return Capability{false, "unavailable", "Native Grok verification is qualified only on macOS arm64 with a pinned native build."}
	default:
		return Capability{false, "unavailable", "This harness has no qualified verification adapter."}
	}
}
