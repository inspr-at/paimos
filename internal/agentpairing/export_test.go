// SPDX-License-Identifier: AGPL-3.0-only

package agentpairing

// VerificationCapabilitiesForTest exposes the server qualification map. The
// comparison lives in an external test: an in-package test that imports agentd
// cycles through nativesessions/stream back into this package.
func VerificationCapabilitiesForTest(platform, arch string) map[string]VerificationCapability {
	return verificationCapabilities(platform, arch)
}
