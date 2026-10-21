// SPDX-License-Identifier: AGPL-3.0-only

package nativequalification

import (
	"testing"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

// FixtureForTest and the SDK pins are shared with the external lead-boundary
// test. That test imports agentd, which cycles back here through consent when
// the test is in this package.
func FixtureForTest(t *testing.T) (Candidate, nativecontract.NativeIdentity, string) {
	t.Helper()
	return fixture(t)
}

func DigestForTest(data []byte) string { return digest(data) }

func UIDForTest(n string) string { return uid(n) }

func SDKBytesForTest() []byte { return sdkFixture }

func SDKDigestForTest() string { return sdkDigest }

func SDKProtocolForTest() string { return sdkProtocol }
