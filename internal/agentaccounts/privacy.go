// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/inspr-at/paimos/internal/accountprivacy"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Buffer finite account responses so every existing and future account route
// crosses one privacy boundary, including mutation responses. Errors and the
// enrolling daemon's quota-key/statusline protocol keep their existing shape.
func (m *Module) privateResponse(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/quota-key") || strings.HasSuffix(r.URL.Path, "/statusline") {
			next(w, r)
			return
		}
		p, ok := tenant.PrincipalFrom(r.Context())
		if !ok {
			next(w, r)
			return
		}
		out := &privacyResponse{header: make(http.Header)}
		next(out, r)
		if out.overflow {
			httpapi.WriteError(w, 503, "account response exceeds safe size")
			return
		}
		if out.status == 0 {
			out.status = 200
		}
		body := out.body.Bytes()
		if out.status >= 200 && out.status < 300 && len(body) > 0 {
			fallback := r.PathValue("accountId")
			ids, err := accountprivacy.IDs(body, fallback)
			if err != nil {
				httpapi.WriteError(w, 500, "account privacy failed")
				return
			}
			if len(ids) > 0 {
				err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
					policy, err := accountprivacy.Load(r.Context(), tx, p, ids)
					if err != nil {
						return err
					}
					body, err = accountprivacy.Redact(body, policy, fallback, strings.HasSuffix(r.URL.Path, "/readings"))
					return err
				})
				if err != nil {
					httpapi.WriteError(w, 500, "account privacy failed")
					return
				}
			}
		}
		for key, values := range out.header {
			w.Header()[key] = values
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Del("Content-Length")
		w.WriteHeader(out.status)
		_, _ = w.Write(body)
	}
}

type privacyResponse struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (w *privacyResponse) Header() http.Header { return w.header }
func (w *privacyResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *privacyResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.body.Len()+len(body) > 4<<20 {
		w.overflow = true
		return len(body), nil
	}
	return w.body.Write(body)
}
