// SPDX-License-Identifier: AGPL-3.0-only
package attachwatch

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func TestLocalConsentProofBindsKeyDigestAndNonce(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub := base64.StdEncoding.EncodeToString(elliptic.Marshal(key.Curve, key.X, key.Y))
	digest, nonce := strings.Repeat("a", 64), strings.Repeat("b", 64)
	raw, err := ecdsa.SignASN1(rand.Reader, key, LocalConsentHash(digest, nonce))
	if err != nil {
		t.Fatal(err)
	}
	sig := base64.StdEncoding.EncodeToString(raw)
	if !VerifyLocalConsent(pub, digest, nonce, sig) {
		t.Fatal("valid proof rejected")
	}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	wrong := base64.StdEncoding.EncodeToString(elliptic.Marshal(other.Curve, other.X, other.Y))
	for _, in := range [][4]string{
		{wrong, digest, nonce, sig}, {pub, strings.Repeat("c", 64), nonce, sig},
		{pub, digest, strings.Repeat("c", 64), sig}, {pub, digest, nonce, "true"},
		{pub, digest, "", sig}, {pub, digest, nonce, sig + "\n"}, {"", digest, nonce, sig},
	} {
		if VerifyLocalConsent(in[0], in[1], in[2], in[3]) {
			t.Fatal("forged or unbound proof accepted")
		}
	}
}
