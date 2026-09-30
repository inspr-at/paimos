// SPDX-License-Identifier: AGPL-3.0-only
package attachwatch

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// LocalConsentHash is domain separated from snapshots and browser approval.
// ConsentDigest already binds the request, computer, process and selected mode.
func LocalConsentHash(consentDigest, nonce string) []byte {
	h := sha256.Sum256([]byte("aeon.attach.local-consent.v1\x00" + consentDigest + "\x00" + nonce))
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

func VerifyLocalConsent(publicKey, consentDigest, nonce, signature string) bool {
	key := LocalAuthPublicKey(publicKey)
	if key == nil || !LocalAuthNonceValid(consentDigest) || !LocalAuthNonceValid(nonce) || len(signature) > 144 || strings.ContainsAny(signature, "\r\n") {
		return false
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(signature)
	return err == nil && ecdsa.VerifyASN1(key, LocalConsentHash(consentDigest, nonce), raw)
}
