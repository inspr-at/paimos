// SPDX-License-Identifier: AGPL-3.0-only

package features

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type failure struct {
	status  int
	message string
}

func (f failure) Error() string              { return f.message }
func fault(status int, message string) error { return failure{status, message} }

func validActor(p tenant.Principal) error {
	if p.ID == "" || p.TenantID == "" {
		return fault(401, "unauthorized")
	}
	return nil
}

func validProjectID(id string) error {
	if id == "" {
		return nil
	}
	invalid := fault(400, "project_id must be a UUID")
	if len(id) != 36 {
		return invalid
	}
	for i, c := range id {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return invalid
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return invalid
			}
		}
	}
	return nil
}

func (s *Service) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/features", s.evaluateHTTP)
	mux.HandleFunc("GET /api/settings/features", s.settingsHTTP)
	mux.HandleFunc("PUT /api/settings/features/{key}", s.saveHTTP)
}

func writeError(w http.ResponseWriter, err error) {
	var f failure
	switch {
	case errors.As(err, &f):
		httpapi.WriteError(w, f.status, f.message)
	case errors.Is(err, authz.ErrForbidden):
		authz.WriteForbidden(w, err)
	default:
		httpapi.WriteError(w, 500, "feature settings unavailable")
	}
}

func (s *Service) evaluateHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, _ := tenant.PrincipalFrom(r.Context())
	projectID := r.URL.Query().Get("project_id")
	items, err := s.Evaluate(r.Context(), p, projectID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"project_id": nullProject(projectID), "items": items})
}

func (s *Service) settingsHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, _ := tenant.PrincipalFrom(r.Context())
	projectID := r.URL.Query().Get("project_id")
	items, err := s.Settings(r.Context(), p, projectID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"project_id": nullProject(projectID), "items": items})
}

func (s *Service) saveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// RawMessage distinguishes required null (inherit) from an omitted value.
	var in struct {
		Enabled          json.RawMessage `json:"enabled"`
		ExpectedRevision *int64          `json:"expected_revision"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.Decode(new(any)) != io.EOF || len(in.Enabled) == 0 || in.ExpectedRevision == nil {
		writeError(w, fault(400, "enabled and expected_revision are required"))
		return
	}
	var enabled *bool
	if err := json.Unmarshal(in.Enabled, &enabled); err != nil {
		writeError(w, fault(400, "enabled must be true, false or null"))
		return
	}
	p, _ := tenant.PrincipalFrom(r.Context())
	out, err := s.Save(r.Context(), p, r.PathValue("key"), r.URL.Query().Get("project_id"), Write{enabled, *in.ExpectedRevision})
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
