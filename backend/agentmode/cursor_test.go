package agentmode

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/secretvault"
)

func TestCursorFixedWidthRandomTamperExpiryAndBinding(t *testing.T) {
	t.Setenv("PAIMOS_SECRET_KEY", base64.StdEncoding.EncodeToString(bytesOf(0x2a, 32)))
	secretvault.ResetForTest()
	t.Cleanup(secretvault.ResetForTest)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	clock := delivery.ClockFunc(func() time.Time { return now })
	codec := NewCursorCodecWithCrypt(clock, time.Minute, nil, nil)
	binding := CursorBinding{UserID: 41, PermissionsEpoch: 9}
	copy(binding.PermissionDigest[:], bytesOf(1, 32))
	copy(binding.RouteDigest[:], bytesOf(2, 32))
	copy(binding.FilterDigest[:], bytesOf(3, 32))

	first, err := codec.Encode(binding, 123)
	if err != nil {
		t.Fatal(err)
	}
	second, err := codec.Encode(binding, 123)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != CursorEncodedLength || len(second) != CursorEncodedLength || first == second {
		t.Fatalf("cursor width/randomness first=%d second=%d equal=%v", len(first), len(second), first == second)
	}
	claims, err := codec.Decode(first, binding)
	if err != nil || claims.HighWater != 123 || !claims.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("decode claims=%+v err=%v", claims, err)
	}

	// Raw URL base64's last character carries four data bits and two unused
	// bits at this fixed width. The permissive decoder accepts three alternate
	// spellings for the identical authenticated ciphertext; every noncanonical
	// spelling must still be rejected as token-string tampering.
	sealed, err := base64.RawURLEncoding.DecodeString(first)
	if err != nil {
		t.Fatal(err)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	aliases := 0
	for _, final := range alphabet {
		candidate := first[:len(first)-1] + string(final)
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(candidate)
		if decodeErr != nil || candidate == first || !bytes.Equal(decoded, sealed) {
			continue
		}
		aliases++
		if _, err := codec.Decode(candidate, binding); err == nil {
			t.Fatalf("noncanonical same-ciphertext cursor ending %q decoded", final)
		}
	}
	if aliases != 3 {
		t.Fatalf("same-ciphertext aliases=%d, want 3", aliases)
	}

	index := len(first) - 2
	replacement := byte('A')
	if first[index] == replacement {
		replacement = 'B'
	}
	tampered := first[:index] + string(replacement) + first[index+1:]
	if _, err := codec.Decode(tampered, binding); err == nil {
		t.Fatal("tampered cursor decoded")
	}
	wrong := binding
	wrong.FilterDigest[0] ^= 1
	if _, err := codec.Decode(first, wrong); err == nil {
		t.Fatal("wrong filter binding decoded")
	}
	now = now.Add(time.Minute)
	if _, err := codec.Decode(first, binding); err == nil {
		t.Fatal("cursor remained valid at its exclusive expiry boundary")
	}
	if _, err := codec.Decode(strings.Repeat("a", CursorEncodedLength-1), binding); err == nil {
		t.Fatal("wrong-width cursor decoded")
	}
}

func TestCursorRejectsEveryNoncanonicalRawURLTerminalAlias(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	clock := delivery.ClockFunc(func() time.Time { return now })
	binding := CursorBinding{UserID: 77, PermissionsEpoch: 3}
	copy(binding.PermissionDigest[:], bytesOf(1, 32))
	copy(binding.RouteDigest[:], bytesOf(2, 32))
	copy(binding.FilterDigest[:], bytesOf(3, 32))
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	totalAliases := 0
	for terminalNibble := 0; terminalNibble < 16; terminalNibble++ {
		var plain []byte
		sealed := make([]byte, cursorCiphertextLength)
		sealed[0] = 1
		sealed[len(sealed)-1] = byte(terminalNibble)
		codec := NewCursorCodecWithCrypt(clock, time.Minute,
			func(_ string, input []byte) ([]byte, error) {
				plain = append([]byte(nil), input...)
				return append([]byte(nil), sealed...), nil
			},
			func(_ string, _ []byte) ([]byte, error) {
				return append([]byte(nil), plain...), nil
			})
		token, err := codec.Encode(binding, int64(terminalNibble))
		if err != nil {
			t.Fatal(err)
		}
		if claims, err := codec.Decode(token, binding); err != nil || claims.HighWater != int64(terminalNibble) {
			t.Fatalf("nibble=%d canonical claims=%+v err=%v", terminalNibble, claims, err)
		}
		decodedCanonical, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || !bytes.Equal(decodedCanonical, sealed) {
			t.Fatalf("nibble=%d canonical decode mismatch: %v", terminalNibble, err)
		}
		aliases := 0
		for _, final := range alphabet {
			candidate := token[:len(token)-1] + string(final)
			decoded, decodeErr := base64.RawURLEncoding.DecodeString(candidate)
			if decodeErr != nil || candidate == token || !bytes.Equal(decoded, decodedCanonical) {
				continue
			}
			aliases++
			totalAliases++
			if _, err := codec.Decode(candidate, binding); err == nil {
				t.Fatalf("nibble=%d noncanonical terminal %q decoded", terminalNibble, final)
			}
		}
		if aliases != 3 {
			t.Fatalf("nibble=%d aliases=%d, want 3", terminalNibble, aliases)
		}
	}
	if totalAliases != 48 {
		t.Fatalf("noncanonical aliases=%d, want 48", totalAliases)
	}
}

func bytesOf(value byte, count int) []byte {
	out := make([]byte, count)
	for index := range out {
		out[index] = value
	}
	return out
}
