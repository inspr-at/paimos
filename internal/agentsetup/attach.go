// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import "errors"

// These startup failures preserve the local pairing boundary without exposing
// lifecycle proofs or pairing state in diagnostics.
var (
	ErrAttachComputerDraining = errors.New("paired computer is disconnecting")
	ErrAttachPairingCleaned   = errors.New("paired computer cleanup completed")
	ErrAttachConfigMismatch   = errors.New("paired attach origin or workspace configuration changed")
)

// ReadAttachProof is for daemon-start registration only: it binds the lifecycle
// proof and public hostname to the already approved runtime. Signed Mac builds
// load it from the daemon-restricted Keychain; legacy/Linux storage is readable
// by same-user code. It never authorizes watch exchanges. Never return it to a CLI
// client, put it in a process argument, or include it in diagnostics.
func ReadAttachProof(root string, c RuntimeConfig) (host string, proof secret, err error) {
	store, err := OpenStore(root, false)
	if err != nil {
		return "", "", err
	}
	defer store.Close()
	engine := Engine{Store: store}
	s, err := engine.load()
	if err != nil {
		return "", "", err
	}
	if s.ComputerCleaned {
		return "", "", ErrAttachPairingCleaned
	}
	if s.DisconnectAll {
		return "", "", ErrAttachComputerDraining
	}
	if s.Origin != c.Origin || s.View.TenantID != c.TenantID || s.View.ComputerID != c.ComputerID || s.View.PrincipalID != c.PrincipalID || s.Request.Workspace != c.Workspace {
		return "", "", ErrAttachConfigMismatch
	}
	return s.Request.ComputerName, s.Lifecycle, nil
}
