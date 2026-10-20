// SPDX-License-Identifier: AGPL-3.0-only
package agentd

// capacityPin is release-owned evidence for an executable and its idle exchange.
// It is never populated from enrollment, environment variables or local config.
// Version is the version qualified by OPS, bound to these exact bytes; checking
// it never executes an unqualified binary (including with --version).
type capacityPin struct {
	Harness string
	Version string
	Path    string
	SHA256  string
	Helpers []capacityHelperPin
}

type capacityHelperPin struct {
	Name   string // "node" only when the qualified exchange needs an interpreter.
	Path   string
	SHA256 string
}

// productionCapacityPins is the single reviewed production table, loaded when
// adapters are constructed at startup. Deliberately empty until OPS supplies
// quota-neutral exchange, startup/config and owned-termination evidence. Native
// Codex entries name the physical executable, never the enrolled wrapper/JS.
func productionCapacityPins() []capacityPin {
	return []capacityPin{}
}

// matches checks the capability against its reviewed entry before hashing the
// executable and every helper at use time. The adapter's launch path is unrelated.
func (p *capacityPin) matches(harness, path, digest, version, nodePath, nodeDigest string) bool {
	if p == nil || p.Harness != harness || p.Version == "" || p.Version != version || p.Path != path || p.SHA256 != digest ||
		len(p.Helpers) > 16 || !exactCapacityBinary(path, digest) {
		return false
	}
	nodeFound := false
	for _, h := range p.Helpers {
		if h.Name == "" || !exactCapacityBinary(h.Path, h.SHA256) {
			return false
		}
		if h.Name == "node" {
			if nodeFound || h.Path != nodePath || h.SHA256 != nodeDigest {
				return false
			}
			nodeFound = true
		}
	}
	return nodeFound || nodePath == "" && nodeDigest == ""
}

func loadCodexCapacityPin(pins []capacityPin) *codexIdleCapability {
	for _, pin := range pins {
		// The approved Codex exchange always executes the native binary directly.
		c := &codexIdleCapability{pin: &pin, binaryPath: pin.Path, binarySHA256: pin.SHA256, version: pin.Version,
			startupHooks: true, inheritedConfig: true, termination: true}
		if c.pin.matches(Codex, c.binaryPath, c.binarySHA256, c.version, "", "") {
			return c
		}
	}
	return nil
}
