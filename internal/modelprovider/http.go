// SPDX-License-Identifier: AGPL-3.0-only

package modelprovider

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type failure struct {
	status  int
	message string
}

func (f failure) Error() string              { return f.message }
func fault(status int, message string) error { return failure{status, message} }

func (s *Service) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/model-provider", s.authorized(s.get))
	mux.HandleFunc("PUT /api/settings/model-provider", s.authorized(s.put))
	mux.HandleFunc("POST /api/settings/model-provider/test", s.authorized(s.test))
}

func (s *Service) authorized(fn func(http.ResponseWriter, *http.Request, tenant.Principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		p, ok := tenant.PrincipalFrom(r.Context())
		if !ok || p.ID == "" || p.TenantID == "" {
			httpapi.WriteError(w, 401, "unauthorized")
			return
		}
		if p.Kind != tenant.Person {
			httpapi.WriteError(w, 403, "person required")
			return
		}
		err := db.InTenant(r.Context(), s.pool, p.TenantID, func(tx pgx.Tx) error {
			return authz.RequireTx(r.Context(), tx, p, "settings.manage", authz.Scope{})
		})
		if err != nil {
			writeError(w, err)
			return
		}
		fn(w, r, p)
	}
}

func writeError(w http.ResponseWriter, err error) {
	var f failure
	switch {
	case errors.As(err, &f):
		httpapi.WriteError(w, f.status, f.message)
	case errors.Is(err, authz.ErrForbidden):
		authz.WriteForbidden(w, err)
	default:
		httpapi.WriteError(w, 500, "internal error")
	}
}

func decode(w http.ResponseWriter, r *http.Request, out any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fault(400, "invalid provider settings")
	}
	if d.Decode(new(any)) != io.EOF {
		return fault(400, "invalid provider settings")
	}
	return nil
}

func (s *Service) get(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var out Config
	err := db.InTenant(r.Context(), s.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = Load(r.Context(), tx)
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func (s *Service) put(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var in struct {
		Settings
		ExpectedRevision *int64  `json:"expected_revision"`
		APIKey           *Secret `json:"api_key,omitempty"`
	}
	if err := decode(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.ExpectedRevision == nil {
		writeError(w, fault(400, "expected revision is required"))
		return
	}
	out, err := s.Save(r.Context(), p, Write{Settings: in.Settings, ExpectedRevision: *in.ExpectedRevision, APIKey: in.APIKey})
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func (s *Service) test(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var in struct {
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if err := decode(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.ExpectedRevision == nil || *in.ExpectedRevision < 0 {
		writeError(w, fault(400, "expected revision is required"))
		return
	}
	c, key, err := s.resolve(r.Context(), p.TenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	if c.Revision != *in.ExpectedRevision || c.BaseURL == "" || c.ChatModel == "" {
		writeError(w, fault(409, "save the provider configuration before testing"))
		return
	}
	_, err = s.chat(r.Context(), c, key, []Message{{Role: "user", Content: "Reply OK."}}, 8)
	if err != nil {
		writeError(w, fault(502, "provider connection test failed"))
		return
	}
	httpapi.WriteJSON(w, 200, map[string]bool{"ok": true})
}
