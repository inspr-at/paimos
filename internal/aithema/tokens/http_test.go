// SPDX-License-Identifier: AGPL-3.0-only

package tokens_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/httpapi"
)

func TestPublicJWKSWithAuthenticationMiddleware(t *testing.T) {
	keys, err := tokens.New(t.Context(), &tokens.MemoryStore{TenantID: "owner"}, bytes.Repeat([]byte{7}, 32), tokens.Config{Issuer: "https://host.example", Audience: "host.example"})
	if err != nil {
		t.Fatal(err)
	}
	am, err := auth.New(auth.Config{SessionKey: bytes.Repeat([]byte{8}, 32)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := &httpapi.Server{Modules: []httpapi.Module{&tokens.Module{Keys: keys}}, Middleware: []func(http.Handler) http.Handler{am.Middleware}}
	handler := srv.Handler()
	call := func(method, path, etag string, stale bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		if stale {
			req.AddCookie(&http.Cookie{Name: "aeon_session", Value: "invalid-fixture-session"})
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	first := call("GET", "/api/aithema/jwks", "", false)
	if first.Code != 200 || first.Header().Get("Content-Type") != "application/jwk-set+json" || first.Header().Get("Cache-Control") != "public, max-age=60, must-revalidate" {
		t.Fatalf("JWKS status/cache: %d", first.Code)
	}
	var set map[string][]map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &set); err != nil {
		t.Fatal(err)
	}
	if len(set) != 1 || len(set["keys"]) != 2 {
		t.Fatal("public key set shape")
	}
	for _, key := range set["keys"] {
		if len(key) != 6 || key["kty"] != "OKP" || key["crv"] != "Ed25519" || key["alg"] != "EdDSA" || key["use"] != "sig" || key["kid"] == "" || key["x"] == "" {
			t.Fatal("private or invalid key fields")
		}
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag absent")
	}
	for _, path := range []string{"/api/aithema/jwks", "/api/aithema/%6awks"} {
		for _, stale := range []bool{false, true} {
			rec := call("GET", path, "", stale)
			if rec.Code != 200 || rec.Body.String() != first.Body.String() || len(rec.Header().Values("Set-Cookie")) != 0 {
				t.Fatalf("public discovery varied: %d", rec.Code)
			}
			for _, tag := range []string{etag, "W/" + etag, `"different", ` + etag, "*"} {
				cached := call("GET", path, tag, stale)
				if cached.Code != 304 || cached.Body.Len() != 0 || cached.Header().Get("ETag") != etag || len(cached.Header().Values("Set-Cookie")) != 0 {
					t.Fatal("conditional cache behavior")
				}
			}
		}
	}
	head := call("HEAD", "/api/aithema/jwks", "", false)
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("ETag") != etag {
		t.Fatal("HEAD behavior")
	}
	if err := keys.Rotate(t.Context()); err != nil {
		t.Fatal(err)
	}
	rotated := call("GET", "/api/aithema/jwks", etag, false)
	if rotated.Code != 200 || rotated.Header().Get("ETag") == etag {
		t.Fatal("rotation did not invalidate ETag")
	}
	for _, path := range []string{"/api/aithema/jwks/unknown", "/api/aithema/keys"} {
		if rec := call("GET", path, "", false); rec.Code != 401 {
			t.Fatalf("public auth exemption leaked to %s: %d", path, rec.Code)
		}
	}
}
func TestJWKSUnavailable(t *testing.T) {
	mux := http.NewServeMux()
	(&tokens.Module{}).Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/aithema/jwks", nil))
	if rec.Code != 503 || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), `"code":"unavailable"`) {
		t.Fatal("unavailable discovery response")
	}
}
