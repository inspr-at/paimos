// SPDX-License-Identifier: AGPL-3.0-only

// Package agentcompat defines the server-owned agent compatibility window.
package agentcompat

import (
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/version"
)

const Protocol = "pairing-v1"

// MinimumVersion is the known baseline for this server's agent protocols.
// Keep it pinned across server releases; raise it only for an incompatible
// agent requirement with qualification evidence. There is no upper bound.
const MinimumVersion = "261001072608.0.0"

type Policy struct {
	Protocol      string `json:"protocol"`
	MinVersion    string `json:"min_version"`
	VersionScheme string `json:"version_scheme"`
}

type Release struct {
	Protocol      string `json:"protocol"`
	Version       string `json:"version"`
	VersionScheme string `json:"version_scheme"`
}

type Result struct {
	Status string `json:"status"`
	Action string `json:"action"`
}

func Supported() Policy {
	return Policy{Protocol: Protocol, MinVersion: MinimumVersion, VersionScheme: version.Scheme}
}

func Current() Release {
	return Release{Protocol: Protocol, Version: version.Version, VersionScheme: version.Scheme}
}

// Check never infers a version scheme from punctuation or orders across eras.
// Missing, invalid and development reports remain unknown. Advice is separate
// from enrollment and cleanup so an update warning cannot revoke active work.
func (p Policy) Check(agent Release) Result {
	unknown := Result{Status: "unknown"}
	if p.Protocol == "" || p.VersionScheme != version.Scheme || !releasehistory.ValidVersion(p.MinVersion) || agent.Protocol == "" {
		return unknown
	}
	update := "Update aeon-agentd to at least " + p.MinVersion + "."
	if agent.Protocol != p.Protocol {
		return Result{Status: "protocol_mismatch", Action: update + " Use a release supporting " + p.Protocol + "."}
	}
	if agent.VersionScheme != p.VersionScheme || !releasehistory.ValidVersion(agent.Version) {
		return unknown
	}
	// Both values are validated coordinates in the same declared scheme.
	if agent.Version < p.MinVersion {
		return Result{Status: "update_required", Action: update}
	}
	return Result{Status: "compatible"}
}
