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

func TestVaultStoredAttributesOnLegacySecItemPath(t *testing.T) {
	for _, inject := range []bool{false, true} {
		// The second case checks that the fake discards accessibility even when
		// injected, matching Apple's legacy backend rather than the add dictionary.
		got := vaultLegacyStoredAttributes("fixture-account", inject)
		for bit, claim := range map[int]string{
			1: "generic password", 2: "pairing service", 4: "retained ACL",
			8: "password data", 16: "no accessibility class", 32: "no sync attribute",
			64: "legacy backend attributes", 128: "add selects legacy non-sync storage without accessibility",
		} {
			if got&bit == 0 {
				t.Errorf("inject=%t: stored item missing %s", inject, claim)
			}
		}
	}
	if vaultLegacyStoredAttributes("", false) != 0 {
		t.Fatal("missing account looked like a stored item")
	}
}
