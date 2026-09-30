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
func (unavailableSigner) Sign(context.Context, string, []byte, string) (string, error) {
	return "", ErrUnavailable
}
