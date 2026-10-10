// SPDX-License-Identifier: AGPL-3.0-only

// Package hookcap defines content-free attached hook qualification. A reported
// harness name, an installed entry or a successful managed run is not evidence
// of a qualified attached hook. This record never grants messaging permission.
package hookcap

import "regexp"

type Capability struct {
	Harness  string `json:"harness"`
	Version  string `json:"version"`
	OS       string `json:"os"`
	Verified bool   `json:"verified"`
	Blocker  string `json:"blocker"`
}

// Qualified is the release-owned ceiling shared by setup and the server.
// No native event/launch-chain qualification evidence has landed yet. In
// particular Codex must not inherit Claude's qualification. Add exact versions
// only with the coordinator's native S2-4/S2-5 evidence, never an env override.
func Qualified(harness, version, goos string) string {
	if goos != "darwin" && goos != "linux" {
		return "unsupported_platform"
	}
	if harness != "claude" && harness != "codex" {
		return "unsupported_harness"
	}
	if version == "" {
		return "unsupported_version"
	}
	return "qualification_pending"
}

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]{0,80}$`)

func Valid(c Capability) bool {
	if !versionPattern.MatchString(c.Version) || (c.OS != "darwin" && c.OS != "linux") {
		return false
	}
	switch c.Harness {
	case "claude", "codex", "cursor", "grok", "pi":
	default:
		return false
	}
	switch c.Blocker {
	case "", "feature_disabled", "qualification_pending", "unsupported_harness", "unsupported_version", "unsupported_platform", "artifact_untrusted", "artifact_changed", "config_environment", "project_override", "config_provenance", "project_scope", "settings_changed", "ownership_unknown", "repair_required":
	default:
		return false
	}
	return c.Verified == (c.Blocker == "")
}

// Project narrows a valid local report. Server consumers still need fresh
// per-attachment process/config checks and a separate owner messaging grant.
func Project(c Capability) Capability {
	if c.Verified {
		if reason := Qualified(c.Harness, c.Version, c.OS); reason != "" {
			c.Verified, c.Blocker = false, reason
		}
	}
	return c
}
