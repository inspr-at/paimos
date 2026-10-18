// SPDX-License-Identifier: AGPL-3.0-only
package config

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhonePushConfigurationIsOptionalStrictAndRedacted(t *testing.T) {
	if cfg, err := phonePushFromFile(""); err != nil || cfg != nil {
		t.Fatal("push should default off")
	}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := PhonePushConfig{PublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), PrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()), Subject: "mailto:ops@example.test"}
	file := filepath.Join(t.TempDir(), "test-vapid.json")
	for _, name := range []string{"valid", "wrong public key", "invalid private key", "invalid subject", "unknown field", "trailing data"} {
		t.Run(name, func(t *testing.T) {
			candidate := cfg
			switch name {
			case "wrong public key":
				candidate.PublicKey = "mismatch"
			case "invalid private key":
				candidate.PrivateKey = "invalid-sensitive-test-value"
			case "invalid subject":
				candidate.Subject = "http://contact.example"
			}
			b, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if name == "unknown field" {
				b = append(b[:len(b)-1], []byte(`,"unexpected":true}`)...)
			}
			if name == "trailing data" {
				b = append(b, []byte(` {}`)...)
			}
			if err := os.WriteFile(file, b, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := phonePushFromFile(file)
			if name == "valid" {
				if err != nil || got == nil || got.PublicKey != cfg.PublicKey {
					t.Fatal("valid VAPID configuration rejected")
				}
				return
			}
			if err == nil || got != nil {
				t.Fatal("invalid VAPID configuration accepted")
			}
			if strings.Contains(err.Error(), candidate.PrivateKey) || strings.Contains(err.Error(), file) {
				t.Fatal("configuration error disclosed credential material or path")
			}
		})
	}
}
