// SPDX-License-Identifier: AGPL-3.0-only
package attachwatch

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// LocalConsentProofVersion is negotiated independently of the attach transport.
// Version 2 binds the canonical Touch ID reason. Pairing keys are unchanged.
const LocalConsentProofVersion = 2

// LocalConsentReason is the Touch ID prompt and part of the signed payload.
// The server recomputes it from the approved snapshot. A different reason
// does not verify. The Secure Enclave does not bind the text a different
// process displays; this stops a signature over any other statement.
func LocalConsentReason(s Snapshot) string {
	action := "watching the conversation"
	if s.Mode == ModeLease {
		action = "status only (no conversation text) for"
	}
	return fmt.Sprintf("Allow %s %s session PID %d on %s", action, s.Harness, s.Process.PID, s.Host)
}

// LocalConsentHash is domain separated from snapshots and browser approval.
// ConsentDigest already binds the request, computer, process and selected mode.
func LocalConsentHash(consentDigest, nonce, reason string) []byte {
	h := sha256.Sum256([]byte("aeon.attach.local-consent.v2\x00" + consentDigest + "\x00" + nonce + "\x00" + reason))
	return h[:]
}

func LocalAuthPublicKey(encoded string) *ecdsa.PublicKey {
	if len(encoded) != 88 {
		return nil
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) != 65 {
		return nil
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), raw)
	if x == nil {
		return nil
	}
	return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
}

func LocalAuthNonceValid(nonce string) bool {
	if len(nonce) != 64 {
		return false
	}
	raw, err := hex.DecodeString(nonce)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == nonce
}

func VerifyLocalConsent(publicKey, consentDigest, nonce, reason, signature string) bool {
	key := LocalAuthPublicKey(publicKey)
	if key == nil || !LocalAuthNonceValid(consentDigest) || !LocalAuthNonceValid(nonce) || reason == "" || strings.ContainsAny(reason, "\x00\r\n") || len(signature) > 144 || strings.ContainsAny(signature, "\r\n") {
		return false
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(signature)
	return err == nil && ecdsa.VerifyASN1(key, LocalConsentHash(consentDigest, nonce, reason), raw)
}
