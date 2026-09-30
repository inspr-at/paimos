// SPDX-License-Identifier: AGPL-3.0-only

package tokens

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const fixtureNow int64 = 1790000000

func fixture(t *testing.T, kind Kind) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/valid/token." + string(kind) + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Doc struct {
			Claims json.RawMessage `json:"claims"`
		} `json:"doc"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f.Doc.Claims
}
func fixtureClaims(t *testing.T, kind Kind) Claims {
	t.Helper()
	c, err := ValidateClaims(kind, fixture(t, kind))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func newTestKeys(t *testing.T, now *int64) (*KeySet, *MemoryStore) {
	t.Helper()
	store := &MemoryStore{TenantID: "tenant-1"}
	keys, err := New(t.Context(), store, bytes.Repeat([]byte{7}, 32), Config{Issuer: "https://host.example", Audience: "host.example", Clock: func() time.Time { return time.Unix(*now, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	return keys, store
}
func activeKey(t *testing.T, k *KeySet) signingKey {
	t.Helper()
	var key signingKey
	if err := k.transact(t.Context(), func(s *keyState, _ int64) error { key = s.Active; return nil }); err != nil {
		t.Fatal(err)
	}
	return key
}
func signRaw(key signingKey, header, payload []byte) string {
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(key.Seed), []byte(input)))
}
func normalHeader(key signingKey) []byte {
	b, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": key.ID, "typ": "JWT"})
	return b
}
func mutate(t *testing.T, raw []byte, fn func(map[string]any)) []byte {
	t.Helper()
	obj, err := decodeObject(raw)
	if err != nil {
		t.Fatal(err)
	}
	fn(obj)
	out, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestContractFixtures(t *testing.T) {
	for _, dir := range []string{"valid", "invalid"} {
		names, err := filepath.Glob("testdata/" + dir + "/token.*.json")
		if err != nil {
			t.Fatal(err)
		}
		if len(names) == 0 {
			t.Fatal("missing fixture vectors")
		}
		for _, name := range names {
			t.Run(name, func(t *testing.T) {
				raw, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				var f struct {
					Doc struct {
						Token  Kind            `json:"token"`
						Claims json.RawMessage `json:"claims"`
					} `json:"doc"`
				}
				if err := json.Unmarshal(raw, &f); err != nil {
					t.Fatal(err)
				}
				_, err = ValidateClaims(f.Doc.Token, f.Doc.Claims)
				if (err == nil) != (dir == "valid") {
					t.Fatalf("fixture validation: %v", err)
				}
			})
		}
	}
}
func TestFixtureProvenance(t *testing.T) {
	raw, err := os.ReadFile("testdata/provenance.json")
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Files map[string]string `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 7 {
		t.Fatalf("fixture count %d", len(p.Files))
	}
	for name, want := range p.Files {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		got := sha256.Sum256(b)
		if hex.EncodeToString(got[:]) != want {
			t.Errorf("fixture changed: %s", name)
		}
	}
}
func TestCanonicalGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/canonical/rfc8785-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Input     string `json:"input_json"`
		Canonical string `json:"canonical"`
		Hex       string `json:"canonical_utf8_hex"`
		SHA       string `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	input := []byte(f.Input)
	copyInput := bytes.Clone(input)
	got, err := CanonicalJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(got)
	if string(got) != f.Canonical || hex.EncodeToString(got) != f.Hex || hex.EncodeToString(digest[:]) != f.SHA {
		t.Fatal("RFC 8785 golden mismatch")
	}
	if !bytes.Equal(input, copyInput) {
		t.Fatal("canonicalizer mutated input")
	}
	for _, bad := range []string{`{"a":1,"a":2}`, `"\ud800"`, `1e999`, `{} {}`} {
		if _, err := CanonicalJSON([]byte(bad)); err == nil {
			t.Errorf("accepted invalid canonical input %q", bad)
		}
	}
}
func TestClosedClaimsAndExactNumbers(t *testing.T) {
	now := fixtureNow
	keys, _ := newTestKeys(t, &now)
	key := activeKey(t, keys)
	for _, kind := range []Kind{Session, Delegated} {
		raw := fixture(t, kind)
		obj, _ := decodeObject(raw)
		for field := range obj {
			t.Run(string(kind)+"/missing/"+field, func(t *testing.T) {
				bad := mutate(t, raw, func(o map[string]any) { delete(o, field) })
				if _, err := ValidateClaims(kind, bad); err == nil {
					t.Fatal("accepted missing required claim")
				}
				if _, err := keys.verify(t.Context(), kind, signRaw(key, normalHeader(key), bad)); err == nil {
					t.Fatal("verified missing required claim")
				}
			})
		}
		changes := map[string]func(map[string]any){
			"unknown":              func(o map[string]any) { o["unknown"] = true },
			"nbf-future":           func(o map[string]any) { o["nbf"] = json.Number("1790001000") },
			"epoch-string":         func(o map[string]any) { o["auth_epoch"] = "3" },
			"epoch-null":           func(o map[string]any) { o["auth_epoch"] = nil },
			"epoch-bool":           func(o map[string]any) { o["auth_epoch"] = true },
			"epoch-zero":           func(o map[string]any) { o["auth_epoch"] = json.Number("0") },
			"epoch-negative":       func(o map[string]any) { o["auth_epoch"] = json.Number("-1") },
			"epoch-fraction":       func(o map[string]any) { o["auth_epoch"] = json.Number("3.0000000000000001") },
			"epoch-unsafe":         func(o map[string]any) { o["auth_epoch"] = json.Number("9007199254740992") },
			"epoch-rounded-unsafe": func(o map[string]any) { o["auth_epoch"] = json.Number("9007199254740991.1") },
			"epoch-huge-exponent":  func(o map[string]any) { o["auth_epoch"] = json.Number("1e999999999") },
			"iat-string":           func(o map[string]any) { o["iat"] = "1790000000" },
			"iat-negative":         func(o map[string]any) { o["iat"] = json.Number("-1") },
			"exp-fraction":         func(o map[string]any) { o["exp"] = json.Number("1790000900.001") },
			"zero-lifetime":        func(o map[string]any) { o["exp"] = o["iat"] },
			"reversed-lifetime":    func(o map[string]any) { o["exp"] = json.Number("1789999999") },
			"long-lifetime":        func(o map[string]any) { o["exp"] = json.Number("1790000901") },
			"issuer-http":          func(o map[string]any) { o["iss"] = "http://host.example" },
			"issuer-space":         func(o map[string]any) { o["iss"] = "https://host. example" },
			"aud-array":            func(o map[string]any) { o["aud"] = []string{"aithema"} },
			"sub-invalid":          func(o map[string]any) { o["sub"] = "person/1" },
			"tid-empty":            func(o map[string]any) { o["tid"] = "" },
			"pid-invalid":          func(o map[string]any) { o["pid"] = "project 1" },
			"sid-invalid":          func(o map[string]any) { o["sid"] = "0f8e9d2c-3b4a-0c5d-8e6f-7a8b9c0d1e2f" },
			"jti-null":             func(o map[string]any) { o["jti"] = nil },
		}
		if kind == Session {
			changes["scope-duplicate"] = func(o map[string]any) { o["scope"] = []string{"session.converse", "session.converse"} }
			changes["scope-empty"] = func(o map[string]any) { o["scope"] = []string{} }
			changes["scope-unlisted"] = func(o map[string]any) { o["scope"] = []string{"nodes.write"} }
			changes["actor-kind"] = func(o map[string]any) { o["actor_kind"] = "agent" }
			changes["host-mode"] = func(o map[string]any) { o["host_mode"] = "accept" }
			changes["generation-extra"] = func(o map[string]any) { o["gen"] = json.Number("1") }
		} else {
			for name, value := range map[string]any{"string": "2", "zero": json.Number("0"), "fraction": json.Number("2.01"), "unsafe": json.Number("9007199254740992")} {
				changes["generation-"+name] = func(o map[string]any) { o["gen"] = value }
			}
			changes["act-extra"] = func(o map[string]any) { o["act"] = map[string]any{"sub": "person-1", "tid": "tenant-1"} }
			changes["act-sub-invalid"] = func(o map[string]any) { o["act"] = map[string]any{"sub": "person/1"} }
			changes["capability-duplicate"] = func(o map[string]any) { o["capabilities"] = []string{"intake.read", "intake.read"} }
			changes["capability-empty"] = func(o map[string]any) { o["capabilities"] = []string{} }
			changes["capability-person-only"] = func(o map[string]any) { o["capabilities"] = []string{"intake.decide"} }
			changes["aud-too-long"] = func(o map[string]any) { o["aud"] = strings.Repeat("x", 257) }
		}
		for name, fn := range changes {
			t.Run(string(kind)+"/"+name, func(t *testing.T) {
				bad := mutate(t, raw, fn)
				if _, err := ValidateClaims(kind, bad); err == nil {
					t.Fatal("accepted invalid claims")
				}
				if _, err := keys.verify(t.Context(), kind, signRaw(key, normalHeader(key), bad)); err == nil {
					t.Fatal("verified invalid claims")
				}
			})
		}
		for _, spelling := range []string{"9007199254740991", "9007199254740991.0", "9.007199254740991e15", "3.0", "3e0"} {
			good := mutate(t, raw, func(o map[string]any) { o["auth_epoch"] = json.Number(spelling) })
			c, err := ValidateClaims(kind, good)
			if err != nil || c.AuthEpoch < 1 {
				t.Errorf("exact integer %s: %v", spelling, err)
			}
		}
	}
	for _, bad := range []string{`{"iss":"https://x","iss":"https://y"}`, `null`, `[]`, `{} {}`, `{"sub":"\ud800"}`} {
		if _, err := ValidateClaims(Session, []byte(bad)); err == nil {
			t.Fatal("accepted malformed JSON")
		}
	}
}
func TestMintVerifyAndTiming(t *testing.T) {
	now := fixtureNow
	keys, _ := newTestKeys(t, &now)
	for _, kind := range []Kind{Session, Delegated} {
		c := fixtureClaims(t, kind)
		minted, err := keys.mint(t.Context(), kind, c)
		if err != nil {
			t.Fatal(err)
		}
		got, err := keys.verify(t.Context(), kind, minted)
		if err != nil || got.TenantID != c.TenantID || got.AuthEpoch != c.AuthEpoch || got.Generation != c.Generation {
			t.Fatalf("round trip %s: %v", kind, err)
		}
		other := Session
		if kind == Session {
			other = Delegated
		}
		if _, err := keys.verify(t.Context(), other, minted); err == nil {
			t.Fatal("cross token kind accepted")
		}
		for _, delta := range []int64{901, 0, -1} {
			bad := c
			bad.ExpiresAt = bad.IssuedAt + delta
			if _, err := keys.mint(t.Context(), kind, bad); err == nil {
				t.Fatal("mint accepted invalid lifetime")
			}
		}
		bad := c
		bad.IssuedAt++
		bad.ExpiresAt++
		if _, err := keys.mint(t.Context(), kind, bad); err == nil {
			t.Fatal("mint accepted future iat")
		}
		bad = c
		bad.Issuer = "https://other.example"
		if _, err := keys.mint(t.Context(), kind, bad); err == nil {
			t.Fatal("mint accepted other issuer")
		}
		bad = c
		bad.Audience = "other.example"
		if _, err := keys.mint(t.Context(), kind, bad); err == nil {
			t.Fatal("mint accepted other audience")
		}
		key := activeKey(t, keys)
		for _, delta := range []int64{60, 61} {
			future := c
			future.IssuedAt = now + delta
			future.ExpiresAt = future.IssuedAt + 600
			raw, _ := json.Marshal(future)
			_, err := keys.verify(t.Context(), kind, signRaw(key, normalHeader(key), raw))
			if (err == nil) != (delta == 60) {
				t.Errorf("future iat delta %d: %v", delta, err)
			}
		}
		for _, field := range []string{"iss", "aud"} {
			raw := mutate(t, fixture(t, kind), func(o map[string]any) { o[field] = "https://other.example" })
			if _, err := keys.verify(t.Context(), kind, signRaw(key, normalHeader(key), raw)); err == nil {
				t.Fatal("verified mismatched issuer/audience")
			}
		}
		now = c.ExpiresAt + 59
		if _, err := keys.verify(t.Context(), kind, minted); err != nil {
			t.Fatal("skew rejected too early")
		}
		now = c.ExpiresAt + 60
		if _, err := keys.verify(t.Context(), kind, minted); err == nil {
			t.Fatal("expiry boundary accepted")
		}
		if _, err := keys.mint(t.Context(), kind, c); err == nil {
			t.Fatal("minted expired token")
		}
		now = fixtureNow
	}
}
func TestAlgorithmAndHeaderConfusion(t *testing.T) {
	now := fixtureNow
	keys, _ := newTestKeys(t, &now)
	key := activeKey(t, keys)
	payload := fixture(t, Session)
	for _, alg := range []string{"none", "HS256", "RS256", "ES256", "eddsa", "Ed25519", ""} {
		raw, _ := json.Marshal(map[string]string{"alg": alg, "kid": key.ID})
		if _, err := keys.VerifySession(t.Context(), signRaw(key, raw, payload)); err == nil {
			t.Errorf("accepted alg %q", alg)
		}
	}
	for _, h := range []string{`{"alg":"EdDSA"}`, `{"alg":"EdDSA","kid":null}`, `{"alg":"EdDSA","kid":"unknown"}`, `{"alg":"EdDSA","kid":"x","crit":["x"]}`, `{"alg":"EdDSA","alg":"HS256","kid":"x"}`} {
		if _, err := keys.VerifySession(t.Context(), signRaw(key, []byte(h), payload)); err == nil {
			t.Fatal("accepted malformed header")
		}
	}
	minted, _ := keys.MintSession(t.Context(), fixtureClaims(t, Session))
	parts := strings.Split(minted, ".")
	badHeader := mutate(t, normalHeader(key), func(o map[string]any) { o["jwk"] = map[string]any{"kty": "OKP"} })
	badType := mutate(t, normalHeader(key), func(o map[string]any) { o["typ"] = "JWS" })
	for _, bad := range []string{
		signRaw(key, badHeader, payload), signRaw(key, badType, payload),
		parts[0] + "." + parts[1] + ".", parts[0] + ".e30." + parts[2],
		parts[0] + "\n." + parts[1] + "." + parts[2], parts[0] + "=." + parts[1] + "." + parts[2],
		minted + ".extra", strings.Repeat("x", maxTokenBytes+1),
	} {
		if _, err := keys.VerifySession(t.Context(), bad); err == nil {
			t.Fatal("accepted malformed or tampered JWT")
		}
	}
	// Attempt HMAC with the advertised Ed25519 public key as the secret.
	hmacHeader, _ := json.Marshal(map[string]string{"alg": "HS256", "kid": key.ID})
	input := base64.RawURLEncoding.EncodeToString(hmacHeader) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key.Public)
	_, _ = mac.Write([]byte(input))
	attack := input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if _, err := keys.VerifySession(t.Context(), attack); err == nil {
		t.Fatal("algorithm confusion")
	}
}
func TestRotationOverlapAndEncryptedRestart(t *testing.T) {
	now := fixtureNow
	keys, store := newTestKeys(t, &now)
	old := activeKey(t, keys)
	initial, _ := keys.JWKS(t.Context())
	minted, err := keys.MintSession(t.Context(), fixtureClaims(t, Session))
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Rotate(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, _ := keys.JWKS(t.Context())
	if len(current.Keys) != 3 || current.Keys[0].ID != initial.Keys[1].ID {
		t.Fatal("next key not promoted or old key missing")
	}
	newToken, err := keys.MintSession(t.Context(), fixtureClaims(t, Session))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Split(newToken, ".")[0] == strings.Split(minted, ".")[0] {
		t.Fatal("rotation did not change kid")
	}
	if _, err := keys.VerifySession(t.Context(), minted); err != nil {
		t.Fatal("old token rejected during overlap")
	}
	now += 959
	if _, err := keys.VerifySession(t.Context(), minted); err != nil {
		t.Fatal("overlap ended early")
	}
	still, _ := keys.JWKS(t.Context())
	if len(still.Keys) != 3 {
		t.Fatal("retired key removed early")
	}
	now++
	expired, _ := keys.JWKS(t.Context())
	if len(expired.Keys) != 2 {
		t.Fatal("retired key retained after retirement")
	}
	// Sign a currently-valid payload with the old test key to isolate retirement
	// rejection from expiration rejection. No private test keys are persisted.
	c := fixtureClaims(t, Session)
	c.IssuedAt = now
	c.ExpiresAt = now + 600
	raw, _ := json.Marshal(c)
	if _, err := keys.VerifySession(t.Context(), signRaw(old, normalHeader(old), raw)); err == nil {
		t.Fatal("retired kid accepted")
	}
	currentKey := activeKey(t, keys)
	if bytes.Contains(store.ciphertext, currentKey.Seed) || bytes.Contains(store.ciphertext, []byte(`"active"`)) {
		t.Fatal("plaintext signing material at rest")
	}
	restart, err := New(t.Context(), store, bytes.Repeat([]byte{7}, 32), keys.cfg)
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ := restart.JWKS(t.Context())
	if loaded.Keys[0].ID != expired.Keys[0].ID {
		t.Fatal("restart changed active key")
	}
	if _, err := New(t.Context(), store, bytes.Repeat([]byte{8}, 32), keys.cfg); err == nil {
		t.Fatal("wrong master accepted")
	}
	other := &MemoryStore{TenantID: "tenant-2", ciphertext: bytes.Clone(store.ciphertext)}
	if _, err := New(t.Context(), other, bytes.Repeat([]byte{7}, 32), keys.cfg); err == nil {
		t.Fatal("cross tenant ciphertext accepted")
	}
	store.ciphertext[len(store.ciphertext)-1] ^= 1
	if _, err := keys.JWKS(t.Context()); err == nil {
		t.Fatal("tampered vault accepted")
	}
}
func TestConcurrentRotationAndDailyRotation(t *testing.T) {
	now := fixtureNow
	keys, store := newTestKeys(t, &now)
	replica, err := New(t.Context(), store, bytes.Repeat([]byte{7}, 32), keys.cfg)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, k := range []*KeySet{keys, replica} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				if err := k.Rotate(context.Background()); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	set, err := keys.JWKS(t.Context())
	if err != nil || len(set.Keys) != 12 {
		t.Fatalf("lost rotation: count %d err %v", len(set.Keys), err)
	}
	before := set.Keys[0].ID
	next := set.Keys[1].ID
	now += 24 * 60 * 60
	set, err = keys.JWKS(t.Context())
	if err != nil || set.Keys[0].ID != next || set.Keys[0].ID == before || len(set.Keys) != 3 {
		t.Fatal("daily rotation or pruning failed")
	}
}
func TestNoJournalImports(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if imp, ok := n.(*ast.ImportSpec); ok {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				for _, segment := range strings.Split(path, "/") {
					if segment == "journal" {
						t.Errorf("journal dependency in %s", name)
					}
				}
			}
			return true
		})
	}
}

func TestSigningConfigurationAndBoundedSkew(t *testing.T) {
	now := fixtureNow
	valid := Config{Issuer: "https://host.example", Audience: "host.example", Clock: func() time.Time { return time.Unix(now, 0) }}
	for _, cfg := range []Config{
		{Issuer: "http://host.example", Audience: "host.example"},
		{Issuer: "https://host.example", Audience: ""},
		{Issuer: "https://host.example", Audience: strings.Repeat("a", 257)},
		{Issuer: "https://host.example", Audience: "host.example", Skew: 61 * time.Second},
		{Issuer: "https://host.example", Audience: "host.example", Skew: -time.Second},
	} {
		if _, err := New(t.Context(), &MemoryStore{TenantID: "owner"}, bytes.Repeat([]byte{7}, 32), cfg); err == nil {
			t.Fatal("accepted invalid signer configuration")
		}
	}
	if _, err := New(t.Context(), nil, bytes.Repeat([]byte{7}, 32), valid); err == nil {
		t.Fatal("nil store accepted")
	}
	if _, err := New(t.Context(), &MemoryStore{TenantID: "owner"}, []byte("short"), valid); err == nil {
		t.Fatal("short master accepted")
	}
	cfg := valid
	cfg.Skew = 10 * time.Second
	keys, err := New(t.Context(), &MemoryStore{TenantID: "owner"}, bytes.Repeat([]byte{7}, 32), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := fixtureClaims(t, Session)
	c.IssuedAt += 11
	c.ExpiresAt += 11
	raw, _ := json.Marshal(c)
	key := activeKey(t, keys)
	if _, err := keys.VerifySession(t.Context(), signRaw(key, normalHeader(key), raw)); err == nil {
		t.Fatal("configured skew exceeded")
	}
}

type failCommitStore struct{ Store }

func (s failCommitStore) Update(ctx context.Context, fn func([]byte) ([]byte, error)) error {
	return s.Store.Update(ctx, func(raw []byte) ([]byte, error) {
		if _, err := fn(raw); err != nil {
			return nil, err
		}
		return nil, ErrUnavailable
	})
}
func TestMintDoesNotReturnTokenOnStoreFailure(t *testing.T) {
	now := fixtureNow
	keys, _ := newTestKeys(t, &now)
	keys.store = failCommitStore{keys.store}
	minted, err := keys.MintSession(t.Context(), fixtureClaims(t, Session))
	if err == nil || minted != "" {
		t.Fatal("token returned without committed key state")
	}
}
