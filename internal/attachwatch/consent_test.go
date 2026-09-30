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
	snap := Snapshot{Host: "Studio Mac", Harness: "codex", Process: Process{PID: 40}}
	reason := LocalConsentReason(snap)
	raw, err := ecdsa.SignASN1(rand.Reader, key, LocalConsentHash(digest, nonce, reason))
	if err != nil {
		t.Fatal(err)
	}
	sig := base64.StdEncoding.EncodeToString(raw)
	if !VerifyLocalConsent(pub, digest, nonce, reason, sig) {
		t.Fatal("valid proof rejected")
	}
	otherReason := LocalConsentReason(Snapshot{Host: "Other Mac", Harness: "codex", Process: Process{PID: 40}})
	if otherReason == reason || VerifyLocalConsent(pub, digest, nonce, otherReason, sig) {
		t.Fatal("signature over a different Touch ID reason was accepted")
	}
	lease := snap
	lease.Mode = ModeLease
	if LocalConsentReason(lease) == reason || !strings.Contains(LocalConsentReason(lease), "status only") {
		t.Fatal("metadata-only reason was not distinct")
	}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	wrong := base64.StdEncoding.EncodeToString(elliptic.Marshal(other.Curve, other.X, other.Y))
	for _, in := range [][5]string{
		{wrong, digest, nonce, reason, sig}, {pub, strings.Repeat("c", 64), nonce, reason, sig},
		{pub, digest, strings.Repeat("c", 64), reason, sig}, {pub, digest, nonce, reason, "true"},
		{pub, digest, "", reason, sig}, {pub, digest, nonce, reason, sig + "\n"}, {"", digest, nonce, reason, sig},
		{pub, digest, nonce, "", sig}, {pub, digest, nonce, reason + "\n", sig},
	} {
		if VerifyLocalConsent(in[0], in[1], in[2], in[3], in[4]) {
			t.Fatal("forged or unbound proof accepted")
		}
	}
}
