// SPDX-License-Identifier: AGPL-3.0-only

// Command paimos-agentd is the operator-local, fenced harness supervisor.
// It authenticates to an AEON server with a scoped agent key. Server module
// wiring stays with the coordinator; this command does not embed the server.
// The coordinator mounts harness.New(pool) as an httpapi.Module and registers
// harness.Plugin() as its manifest. Each child is registered through those
// public routes with a run and work order binding; no server wiring is needed
// here. The daemon key needs harness.write and harness.worker in addition to
// nodes.read for resolving the work order's project.
//
// This repository provides packages.<system>.aeon-agentd in flake.nix. Its
// current Nix executable is bin/aeon-agentd; GitHub release assets are named
// paimos-agentd-<os>-<arch>. Install a matching package, a verified release
// binary, or build from source with the flags below.
//
// # Release binaries
//
// A push of a v* tag (v plus the version.json calendar coordinate) builds
// paimos-agentd for darwin-arm64, darwin-amd64, linux-arm64 and linux-amd64 and attaches
// these assets to that tag's GitHub release:
//
//	paimos-agentd-darwin-arm64
//	paimos-agentd-darwin-amd64
//	paimos-agentd-linux-arm64
//	paimos-agentd-linux-amd64
//	SHA256SUMS
//
// The release build sets CGO_ENABLED=0, passes -trimpath, and injects the
// version.json version field with the same linker setting as the server image:
//
//	-X github.com/inspr-at/paimos/internal/version.Version=<version>
//
// Development builds leave that variable at "dev".
//
// Verify the checksums before installing. From the directory that contains
// the downloaded assets, on Linux:
//
//	sha256sum -c SHA256SUMS
//
// On macOS:
//
//	shasum -a 256 -c SHA256SUMS
//
// Install the matching binary into a new, user-owned versioned directory only
// after verification; see docs/AGENT_INTEGRATION.md for the exact guide.
//
// From source, at the repository root, set VERSION to the version field of
// version.json (no leading v):
//
//	CGO_ENABLED=0 go build -trimpath \
//	  -ldflags "-X github.com/inspr-at/paimos/internal/version.Version=${VERSION}" \
//	  -o paimos-agentd ./cmd/aeon-agentd
//
// Cross-compiling uses the same flags with GOOS and GOARCH set to darwin or
// linux and arm64 or amd64. CGO stays off, so those four targets build
// without a C toolchain. Crossbuilds alone do not qualify user-service behavior.
package main

import "github.com/inspr-at/paimos/internal/version"

func init() {
	// Keep internal/version.Version reachable so release -X ldflags apply.
	if version.Version == "" {
		panic("paimos-agentd version is empty")
	}
}
