// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"crypto/elliptic"
	"encoding/base64"
	"strings"
	"testing"
)

func TestTouchIDStatusNamesPinAndExactPairingUpgrade(t *testing.T) {
	store := testStore(t)
	e := &Engine{Store: store}
	curve := elliptic.P256()
	public := base64.StdEncoding.EncodeToString(elliptic.Marshal(curve, curve.Params().Gx, curve.Params().Gy))
	s := &snapshot{Origin: "https://paired.test", Phase: "connected", View: View{ComputerID: testComputer, ComputerState: "connected"}, Request: DeviceRequest{Details: Details{Platform: "darwin", Workspace: "/tmp/fixture's workspace"}}}
	p := e.progress(s)
	want := "aeon-agentd pair --url 'https://paired.test' --state-root " + touchIDQuote(store.Path()+"-touch-id") + " --workspace '/tmp/fixture'\"'\"'s workspace'"
	if p.TouchIDConfirmation != "needs pairing upgrade" || p.TouchIDUpgradeCommand != want {
		t.Fatal("legacy pairing missing exact safe command")
	}
	s.LocalAuthKeyID = "synthetic-key-id"
	if p = e.progress(s); p.TouchIDConfirmation != "needs pairing upgrade" {
		t.Fatal("key ID alone claimed server pin")
	}
	s.Request.LocalAuthPublicKey = public
	if p = e.progress(s); p.TouchIDConfirmation != "ready (pairing key pinned)" || p.TouchIDUpgradeCommand != "" {
		t.Fatal("pinned pairing not ready")
	}
	s.View.ComputerState = "revoked"
	if p = e.progress(s); !strings.Contains(p.TouchIDConfirmation, "unavailable") || p.TouchIDUpgradeCommand != "" {
		t.Fatal("revoked pairing ready")
	}
	s.View.ComputerState = "connected"
	s.Request.Platform = "linux"
	if p = e.progress(s); !strings.Contains(p.TouchIDConfirmation, "unsupported") {
		t.Fatal("Linux promised Touch ID")
	}
}
