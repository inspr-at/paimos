// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type pendingCode struct {
	authTime      int64
	challenge     string
	nonce         string
	subject       string
	email         string
	name          string
	badNonce      bool
	emailVerified *bool
}

// fakeOIDC is a discovery + JWKS + token endpoint for the authorization-code tests.
type fakeOIDC struct {
	issuer   string
	clientID string
	key      *rsa.PrivateKey
	kid      string

	mu        sync.Mutex
	codes     map[string]pendingCode
	sawSecret bool
	exchanges int
}

func startFakeOIDC(t *testing.T, clientID string) *fakeOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	f := &fakeOIDC{clientID: clientID, key: key, kid: "test", codes: map[string]pendingCode{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	f.issuer = srv.URL
	t.Cleanup(srv.Close)
	return f
}

func (f *fakeOIDC) allow(code, challenge, nonce, subject, email, name string, badNonce bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	verified := true
	f.codes[code] = pendingCode{
		challenge:     challenge,
		nonce:         nonce,
		subject:       subject,
		email:         email,
		name:          name,
		badNonce:      badNonce,
		emailVerified: &verified,
	}
}

func (f *fakeOIDC) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		writeFakeJSON(w, http.StatusOK, map[string]any{
			"issuer":                                f.issuer,
			"authorization_endpoint":                f.issuer + "/authorize",
			"token_endpoint":                        f.issuer + "/token",
			"jwks_uri":                              f.issuer + "/keys",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		})
	case "/keys":
		n := base64.RawURLEncoding.EncodeToString(f.key.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes())
		writeFakeJSON(w, http.StatusOK, map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA", "use": "sig", "alg": "RS256", "kid": f.kid, "n": n, "e": e,
			}},
		})
	case "/token":
		f.token(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOIDC) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeFakeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if r.FormValue("client_secret") != "" {
		f.mu.Lock()
		f.sawSecret = true
		f.mu.Unlock()
	}
	code := r.FormValue("code")
	verifier := r.FormValue("code_verifier")
	f.mu.Lock()
	pend, ok := f.codes[code]
	if ok {
		delete(f.codes, code)
		f.exchanges++
	}
	f.mu.Unlock()
	if !ok || oauth2.S256ChallengeFromVerifier(verifier) != pend.challenge {
		writeFakeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	nonce := pend.nonce
	if pend.badNonce {
		nonce = "wrong-nonce"
	}
	now := time.Now().Unix()
	claims := map[string]any{
		"iss":   f.issuer,
		"sub":   pend.subject,
		"aud":   f.clientID,
		"exp":   now + 3600,
		"iat":   now - 5,
		"nonce": nonce,
		"email": pend.email,
		"name":  pend.name,
	}
	if pend.authTime > 0 {
		claims["auth_time"] = pend.authTime
	}
	if pend.emailVerified != nil {
		claims["email_verified"] = *pend.emailVerified
	}
	raw, err := f.sign(claims)
	if err != nil {
		writeFakeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	writeFakeJSON(w, http.StatusOK, map[string]any{
		"access_token": "access-token",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     raw,
	})
}

func (f *fakeOIDC) sign(claims map[string]any) (string, error) {
	hb, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": f.kid})
	pb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	h := base64.RawURLEncoding.EncodeToString(hb)
	p := base64.RawURLEncoding.EncodeToString(pb)
	sum := sha256.Sum256([]byte(h + "." + p))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return h + "." + p + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func writeFakeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
