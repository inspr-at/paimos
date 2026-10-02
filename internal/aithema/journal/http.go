// SPDX-License-Identifier: AGPL-3.0-only

package journal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
)

// Module uses delegated JWT authentication, independently of person cookies
// and agent API keys. The outer auth route declarations hand authentication
// to this module; they do not make journal data anonymously accessible.
type Module struct {
	Store *Store
	Keys  *tokens.KeySet
}

func (m *Module) Mount(mux *http.ServeMux) {
	for _, route := range []struct{ area, action, method string }{
		{"journal", "records", "POST"}, {"journal", "snapshots", "POST"}, {"journal", "op.result", "POST"},
		{"journal", "records", "GET"}, {"journal", "cursor", "GET"}, {"journal", "authority", "GET"},
		{"ledger", "admit", "POST"}, {"ledger", "claim", "POST"}, {"ledger", "settle", "POST"}, {"ledger", "recover", "POST"}, {"ledger", "holds", "GET"},
	} {
		mux.HandleFunc(route.method+" /api/aithema/"+route.area+"/sessions/{sid}/"+route.action, func(w http.ResponseWriter, r *http.Request) { m.serve(w, r, route.area, route.action) })
	}
}
func writeError(w http.ResponseWriter, err error) {
	f := publicError(err)
	w.WriteHeader(f.Status)
	body := map[string]any{"error": f.Code, "code": f.Code}
	if f.Document != nil {
		body["document"] = f.Document
	}
	_, _ = w.Write(marshal(body))
}
func (m *Module) serve(w http.ResponseWriter, r *http.Request, area, action string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if m.Keys == nil || m.Store == nil {
		writeError(w, fault(503, "unavailable"))
		return
	}
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer ") || strings.ContainsAny(strings.TrimPrefix(auth[0], "Bearer "), " \t\r\n") {
		writeError(w, fault(401, "unauthorized"))
		return
	}
	claims, err := m.Keys.VerifyDelegated(r.Context(), strings.TrimPrefix(auth[0], "Bearer "))
	if err != nil {
		if errors.Is(err, tokens.ErrUnavailable) {
			writeError(w, fault(503, "unavailable"))
		} else {
			writeError(w, fault(401, "unauthorized"))
		}
		return
	}
	if claims.SessionID != r.PathValue("sid") {
		writeError(w, fault(403, "forbidden"))
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, fault(400, "invalid_request"))
		return
	}
	var raw []byte
	if r.Method == http.MethodPost {
		raw, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			var size *http.MaxBytesError
			if errors.As(err, &size) {
				writeError(w, fault(413, "too_large"))
			} else {
				writeError(w, fault(400, "invalid_request"))
			}
			return
		}
	}
	result, err := m.Store.request(r.Context(), claims, area, action, r.Method, raw, query, func() error {
		// Lock waits and body reads must not extend a credential's lifetime.
		// Reuse the signer so its configured clock and skew stay authoritative.
		if _, err := m.Keys.VerifyDelegated(r.Context(), strings.TrimPrefix(auth[0], "Bearer ")); err != nil {
			if errors.Is(err, tokens.ErrUnavailable) {
				return fault(503, "unavailable")
			}
			return fault(401, "unauthorized")
		}
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if len(result.Body) > maxResponseBytes {
		writeError(w, fault(413, "too_large"))
		return
	}
	w.WriteHeader(result.Status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(result.Body)
	}
}
