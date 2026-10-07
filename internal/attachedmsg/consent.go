// SPDX-License-Identifier: AGPL-3.0-only
package attachedmsg

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/inspr-at/paimos/internal/attachwatch"
)

// The grant digest binds the complete immutable attachment/generation/owner/
// hook tuple. This separate domain prevents a watch signature authorizing notes.
func LocalConsentHash(digest, nonce, reason string) []byte {
	h := sha256.Sum256([]byte("aeon.owner-messages.local-consent.v1\x00" + digest + "\x00" + nonce + "\x00" + reason))
	return h[:]
}

func LocalConsentReason(g Grant) string {
	return fmt.Sprintf("Allow my messages at the next step of %s session PID %d on %s (generation %s)", g.Snapshot.Harness, g.Snapshot.Process.PID, g.Snapshot.Host, g.Binding.Generation)
}

func VerifyLocalConsent(publicKey, digest, nonce, reason, signature string) bool {
	key := attachwatch.LocalAuthPublicKey(publicKey)
	if key == nil || !attachwatch.LocalAuthNonceValid(digest) || !attachwatch.LocalAuthNonceValid(nonce) || reason == "" || strings.ContainsAny(reason, "\x00\r\n") || len(signature) > 144 || strings.ContainsAny(signature, "\r\n") {
		return false
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(signature)
	return err == nil && ecdsa.VerifyASN1(key, LocalConsentHash(digest, nonce, reason), raw)
}
