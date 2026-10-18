// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func signed(raw, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(raw)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
func TestAuthenticatedDeliveryEnvelope(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	raw := []byte(`{"action":"opened"}`)
	for _, sig := range []string{"", "sha256=" + strings.Repeat("0", 64)} {
		if _, err := authenticate("pull_request", "delivery-1", sig, raw, secret); err == nil {
			t.Fatal("bad signature accepted")
		}
	}
	if _, err := authenticate("pull_request", "delivery-1", signed(raw, secret), raw, secret); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"action":"opened","action":"closed"}`, `{} {}`, strings.Repeat(`[`, 34) + `0` + strings.Repeat(`]`, 34)} {
		b := []byte(body)
		if _, err := authenticate("pull_request", "delivery-1", signed(b, secret), b, secret); err == nil {
			t.Fatal("ambiguous payload accepted")
		}
	}
	if _, err := authenticate("pull_request", "", signed(raw, secret), raw, secret); err == nil {
		t.Fatal("delivery id missing")
	}
}
