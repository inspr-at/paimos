// SPDX-License-Identifier: AGPL-3.0-only
package attachedmsg

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/attachwatch"
)

func TestMessagingConsentHasIndependentDomainAndTuple(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub := base64.StdEncoding.EncodeToString(elliptic.Marshal(key.Curve, key.X, key.Y))
	g := Grant{Binding: Binding{TenantID: UUID(), ProjectID: UUID(), AttachRequestID: UUID(), SessionID: UUID(), ComputerID: UUID(), OwnerID: UUID(), Generation: UUID(), DaemonEpoch: strings.Repeat("a", 64), HookReleaseDigest: strings.Repeat("b", 64), HookConfigDigest: strings.Repeat("c", 64), HarnessVersion: "test-1", Scope: "owner_messages", ConsentPolicy: "local_auth", SnapshotDigest: strings.Repeat("d", 64), ServiceEpoch: UUID()}}
	digest, nonce, reason := g.Binding.Digest(), strings.Repeat("e", 64), LocalConsentReason(g)
	sign := func(hash []byte) string {
		t.Helper()
		raw, e := ecdsa.SignASN1(rand.Reader, key, hash)
		if e != nil {
			t.Fatal(e)
		}
		return base64.StdEncoding.EncodeToString(raw)
	}
	signature := sign(LocalConsentHash(digest, nonce, reason))
	if !VerifyLocalConsent(pub, digest, nonce, reason, signature) {
		t.Fatal("valid consent refused")
	}
	if VerifyLocalConsent(pub, digest, nonce, reason, sign(attachwatch.LocalConsentHash(digest, nonce, reason))) {
		t.Fatal("watch signature enabled messages")
	}
	changes := []func(*Binding){
		func(b *Binding) { b.TenantID = UUID() }, func(b *Binding) { b.ProjectID = UUID() }, func(b *Binding) { b.AttachRequestID = UUID() },
		func(b *Binding) { b.SessionID = UUID() }, func(b *Binding) { b.ComputerID = UUID() }, func(b *Binding) { b.OwnerID = UUID() },
		func(b *Binding) { b.Generation = UUID() }, func(b *Binding) { b.DaemonEpoch = strings.Repeat("f", 64) },
		func(b *Binding) { b.HookReleaseDigest = strings.Repeat("f", 64) }, func(b *Binding) { b.HookConfigDigest = strings.Repeat("f", 64) },
		func(b *Binding) { b.HarnessVersion = "other" }, func(b *Binding) { b.Scope = "watch" }, func(b *Binding) { b.ConsentPolicy = "aeon" },
		func(b *Binding) { b.SnapshotDigest = strings.Repeat("f", 64) }, func(b *Binding) { b.ServiceEpoch = UUID() },
	}
	for i, change := range changes {
		bad := g.Binding
		change(&bad)
		if VerifyLocalConsent(pub, bad.Digest(), nonce, reason, signature) {
			t.Fatalf("tuple field %d inherited consent", i)
		}
	}
	if VerifyLocalConsent(pub, digest, strings.Repeat("f", 64), reason, signature) || VerifyLocalConsent(pub, digest, nonce, "different prompt", signature) {
		t.Fatal("nonce or prompt substitution verified")
	}
}
