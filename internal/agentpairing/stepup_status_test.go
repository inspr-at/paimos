// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"bytes"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
)

func TestComputerReadinessProjectsActualConfirmationPin(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		var f *fixture
		var computer string
		if pinned {
			varID, _, in, _ := upgradedWatchFixture(t)
			f = varID
			computer = in.ComputerID
		} else {
			varID, _, in := watchFixture(t)
			f = varID
			computer = in.ComputerID
		}
		res := f.call("GET", "/api/agent-pairing/computers/"+computer, nil, true, "", 200)
		var v agentpairing.View
		decodeResult(t, res, &v)
		if v.LocalAuthPinned == nil || *v.LocalAuthPinned != pinned {
			t.Fatal("computer readiness lost the server pin")
		}
		if bytes.Contains(res.Body.Bytes(), []byte("local_auth_public_key")) {
			t.Fatal("readiness unnecessarily exposed key material")
		}
	}
}
