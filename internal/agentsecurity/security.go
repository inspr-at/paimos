// SPDX-License-Identifier: AGPL-3.0-only
// Package agentsecurity owns signed-daemon Keychain and Secure Enclave access.
package agentsecurity

import (
	"context"
	"errors"
)

var ErrUnavailable = errors.New("Secure Enclave unavailable; use Aeon approval")
var ErrDenied = errors.New("signed daemon Keychain access denied")
var ErrExists = errors.New("Keychain item already exists")

// Vault stores private capabilities without exposing them through diagnostics.
// Production selects the macOS implementation at build time; tests inject it.
type Vault interface {
	Read(string) ([]byte, error)
	Write(string, []byte, bool) error
	Delete(string) error
}

// Signer creates a non-exportable P-256 key at pairing. Sign accepts a hash,
// never a caller-supplied signature or a boolean authentication assertion.
type Signer interface {
	Create(context.Context, string) (string, error)
	Sign(context.Context, string, []byte, string) (string, error)
}
