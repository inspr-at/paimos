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
	if _, err := DefaultSigner().Sign(t.Context(), "unsigned-fixture", make([]byte, 32), ""); !errors.Is(err, ErrDenied) {
		t.Fatal("empty Touch ID reason reached Keychain")
	}
}

func TestEnclaveCreateRefusesAnExistingTag(t *testing.T) {
	if got := enclaveCreateDisposition(0); got != -25299 {
		t.Fatalf("existing key disposition %d", got)
	}
	if got := enclaveCreateDisposition(-25300); got != -25300 {
		t.Fatalf("missing key disposition %d", got)
	}
	if got := enclaveCreateDisposition(-25299); got != -25299 {
		t.Fatalf("duplicate status changed to %d", got)
	}
}

func TestVaultAddPinsDeviceOnlyAndNotSynchronizable(t *testing.T) {
	if !vaultAddIsDeviceOnly("fixture-account") {
		t.Fatal("generic password add omitted ThisDeviceOnly or synchronizable=false")
	}
	if vaultAddIsDeviceOnly("") {
		t.Fatal("missing account looked like a protected add")
	}
}
