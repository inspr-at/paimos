//go:build darwin && cgo && aeon_enclave

// SPDX-License-Identifier: AGPL-3.0-only
package agentsecurity

import (
	"errors"
	"testing"
)

func TestUnsignedDaemonCannotAccessKeychainOrEnclave(t *testing.T) {
	// The unsigned test executable must fail before any Keychain or OS prompt.
	vault := DefaultVault()
	if _, err := vault.Read("unsigned-fixture"); !errors.Is(err, ErrDenied) {
		t.Fatal("unsigned read not denied")
	}
	if err := vault.Write("unsigned-fixture", []byte("fixture"), true); !errors.Is(err, ErrDenied) {
		t.Fatal("unsigned write not denied")
	}
	if err := vault.Delete("unsigned-fixture"); !errors.Is(err, ErrDenied) {
		t.Fatal("unsigned delete not denied")
	}
	if _, err := DefaultSigner().Create(t.Context(), "unsigned-fixture"); !errors.Is(err, ErrDenied) {
		t.Fatal("unsigned key creation not denied")
	}
	if _, err := DefaultSigner().Sign(t.Context(), "unsigned-fixture", make([]byte, 32), "fixture"); !errors.Is(err, ErrDenied) {
		t.Fatal("unsigned signing not denied")
	}
}
