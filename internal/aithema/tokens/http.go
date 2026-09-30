// SPDX-License-Identifier: AGPL-3.0-only

package tokens

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// Module publishes the host-wide public keys. A nil KeySet keeps local HTTP
// development compatible and refuses discovery with a noncacheable 503.
type Module struct{ Keys *KeySet }

func (m *Module) Mount(mux *http.ServeMux) { mux.HandleFunc("GET /api/aithema/jwks", m.serveJWKS) }
func (m *Module) serveJWKS(w http.ResponseWriter, r *http.Request) {
	unavailable := func() {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"signing keys unavailable","code":"unavailable"}` + "\n"))
	}
	if m.Keys == nil {
		unavailable()
		return
	}
	jwks, err := m.Keys.JWKS(r.Context())
	if err != nil {
		unavailable()
		return
	}
	raw, err := json.Marshal(jwks)
	if err != nil {
		unavailable()
		return
	}
	digest := sha256.Sum256(raw)
	etag := `"` + hex.EncodeToString(digest[:]) + `"`
	w.Header().Set("Content-Type", "application/jwk-set+json")
	w.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	w.Header().Set("ETag", etag)
	for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(raw)
	}
}
