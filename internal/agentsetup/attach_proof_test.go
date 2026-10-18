// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"errors"
	"testing"
)

func TestReadAttachProofPreservesLocalRefusalCause(t *testing.T) {
	for _, name := range []string{"connected", "disconnecting", "cleaned", "cleaned after disconnect", "origin", "workspace", "tenant", "computer", "principal"} {
		t.Run(name, func(t *testing.T) {
			e, api, _, options, _ := engineFixture(t)
			defer e.Store.Close()
			approveFixture(t, e, api, options)
			config, err := ReadRuntimeConfig(e.Store.Path())
			if err != nil {
				t.Fatal("fixture runtime unavailable")
			}
			saved, err := e.load()
			if err != nil {
				t.Fatal("fixture pairing unavailable")
			}
			var want error
			switch name {
			case "disconnecting":
				saved.DisconnectAll, want = true, ErrAttachComputerDraining
			case "cleaned", "cleaned after disconnect":
				saved.ComputerCleaned, want = true, ErrAttachPairingCleaned
				saved.DisconnectAll = name == "cleaned after disconnect"
			case "origin":
				config.Origin, want = "https://other.invalid", ErrAttachConfigMismatch
			case "workspace":
				config.Workspace, want = physicalTemp(t), ErrAttachConfigMismatch
			case "tenant":
				config.TenantID, want = "other", ErrAttachConfigMismatch
			case "computer":
				config.ComputerID, want = "other", ErrAttachConfigMismatch
			case "principal":
				config.PrincipalID, want = "other", ErrAttachConfigMismatch
			}
			if err := e.save(saved, false); err != nil {
				t.Fatal("fixture state not saved")
			}
			host, proof, err := ReadAttachProof(e.Store.Path(), config)
			if !errors.Is(err, want) {
				t.Fatal("local refusal cause lost")
			}
			if want != nil && (host != "" || proof != "") {
				t.Fatal("refused startup returned pairing authority")
			}
			if want == nil && (host == "" || proof == "") {
				t.Fatal("connected startup lost pairing authority")
			}
		})
	}
}
