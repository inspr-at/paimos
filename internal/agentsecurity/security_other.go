//go:build !darwin || !cgo || !aeon_enclave

// SPDX-License-Identifier: AGPL-3.0-only
package agentsecurity

import (
	"context"
	"runtime"
)

func LocalAuthCapability() string {
	if runtime.GOOS == "darwin" {
		return "unsigned"
	}
	return "unsupported"
}

func DefaultVault() Vault   { return nil }
func DefaultSigner() Signer { return unavailableSigner{} }

type unavailableSigner struct{}

func (unavailableSigner) Create(context.Context, string) (string, error) { return "", ErrUnavailable }
func (unavailableSigner) Sign(_ context.Context, _ string, hash []byte, reason string) (string, error) {
	if err := localSignReason(reason); err != nil {
		return "", err
	}
	if len(hash) != 32 {
		return "", ErrDenied
	}
	return "", ErrUnavailable
}
