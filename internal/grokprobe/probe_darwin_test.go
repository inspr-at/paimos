// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package grokprobe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func authFixture(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "auth.json")
	scope := map[string]any{"https://auth.x.ai::test-client": map[string]any{
		"auth_mode": "oidc", "oidc_issuer": "https://auth.x.ai", "oidc_client_id": "test-client",
		"principal_type": "User", "user_id": "subject-42", "key": "fixture-bearer-do-not-persist",
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
	}}
	raw, _ := json.Marshal(scope)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTrustedProbeDerivesOnlyVerifiedIdentity(t *testing.T) {
	root := t.TempDir()
	path := authFixture(t, root)
	client := &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != grokUserInfoURL || r.Header.Get("Authorization") != "Bearer fixture-bearer-do-not-persist" {
			t.Fatal("userinfo request left the fixed authenticated boundary")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"sub":"subject-42","email":"verified@example.test","email_verified":true}`))}, nil
	})}
	b := Binding{Variant: "npm-grok-1.0.30", BinaryPath: "/synthetic/node_modules/@xai-official/grok/bin/grok-native", AuthPath: path, ScratchRoot: root}
	verify := func(Binding, string) (string, string, error) { return "synthetic-pin", "/synthetic", nil }
	identity, err := probe(t.Context(), b, client, verify)
	want := sha256.Sum256([]byte("subject-42"))
	if err != nil || identity.Label != "verified@example.test" || identity.Binding.PrincipalSHA256 != hex.EncodeToString(want[:]) {
		t.Fatal("verified principal was not derived")
	}
	raw, _ := json.Marshal(identity)
	if strings.Contains(string(raw), "fixture-bearer") || strings.Contains(string(raw), "subject-42") {
		t.Fatal("secret or raw subject persisted")
	}
	b.PrincipalSHA256 = strings.Repeat("a", 64)
	if _, err := probe(t.Context(), b, client, verify); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatal("expected subject fence weakened")
	}
	client.Transport = fixtureTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"sub":"other","email":"unverified@example.test","email_verified":true}`))}, nil
	})
	if _, err := probe(t.Context(), Binding{Variant: b.Variant, BinaryPath: b.BinaryPath, AuthPath: path, ScratchRoot: root}, client, verify); err == nil {
		t.Fatal("userinfo mismatch accepted")
	}
	client.Transport = fixtureTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"sub":"subject-42","email":"unverified@example.test","email_verified":false}`))}, nil
	})
	identity, err = probe(t.Context(), Binding{Variant: b.Variant, BinaryPath: b.BinaryPath, AuthPath: path, ScratchRoot: root}, client, verify)
	if err != nil || identity.Label != "Grok subject SHA-256: "+hex.EncodeToString(want[:]) {
		t.Fatal("unverified email was exposed or subject identifier unstable")
	}
}

func TestAuthFileAndQualifiedBinaryFailures(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "auth.json")
	if _, _, _, err := ReadAuth(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("missing login accepted")
	}
	path = authFixture(t, root)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ReadAuth(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("insecure mode accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "auth-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ReadAuth(link, strings.Repeat("0", 64)); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, _, err := Variant("generic-grok"); err == nil {
		t.Fatal("unqualified wrapper accepted")
	}
	for _, variant := range []string{"npm-grok-1.0.30", "source-xai-grok-pager-1.0.32"} {
		name, digest, err := Variant(variant)
		if err != nil || name == "" || len(digest) != 64 {
			t.Fatal("qualified variant missing pin")
		}
	}
	_, _, err := VerifyBinding(Binding{Variant: "generic-grok"}, "")
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("unqualified binding accepted")
	}
}
