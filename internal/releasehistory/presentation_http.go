// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// maxPresentationBody bounds a presentation request; six short fields fit easily.
const maxPresentationBody = 16 << 10

func (m *Module) putPresentation(w http.ResponseWriter, r *http.Request) {
	m.writePresentation(w, r, false)
}

func (m *Module) deletePresentation(w http.ResponseWriter, r *http.Request) {
	m.writePresentation(w, r, true)
}

// writePresentation sets or clears one version's presentation for the served
// product project. The version need not be in this build's history yet: the
// release agent writes it before the new build goes live.
func (m *Module) writePresentation(w http.ResponseWriter, r *http.Request, clear bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if m.pool == nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "release presentations are not available on this server")
		return
	}
	version := strings.TrimPrefix(r.PathValue("version"), "v")
	if !ValidVersion(version) {
		httpapi.WriteError(w, http.StatusBadRequest, "not an inspr-calendar-v2 version")
		return
	}
	var in PresentationInput
	if !clear {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPresentationBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			httpapi.WriteError(w, http.StatusBadRequest, "body must be a presentation object")
			return
		}
	}
	var change PresentationChange
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `SET LOCAL lock_timeout = '5s'`); err != nil {
			return err
		}
		project, err := ResolveProject(r.Context(), tx, m.projectKey)
		if err != nil {
			return err
		}
		if err := AuthorizePresentation(r.Context(), tx, p, project); err != nil {
			return err
		}
		if clear {
			change, err = ClearPresentation(r.Context(), tx, p, project, version)
		} else {
			change, err = SavePresentation(r.Context(), tx, p, project, version, in)
		}
		return err
	})
	switch {
	case err == nil:
		w.Header().Set("Cache-Control", "no-store")
		httpapi.WriteJSON(w, http.StatusOK, change)
	case errors.Is(err, pgx.ErrNoRows):
		httpapi.WriteError(w, http.StatusNotFound, "no product project for release presentations")
	case errors.Is(err, ErrPresentationDenied):
		httpapi.WriteError(w, http.StatusForbidden, ErrPresentationDenied.Error())
	case errors.Is(err, ErrPresentationConflict):
		httpapi.WriteError(w, http.StatusConflict, ErrPresentationConflict.Error())
	case errors.Is(err, ErrPresentationInvalid):
		httpapi.WriteError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		httpapi.WriteError(w, http.StatusInternalServerError, "release presentation not saved")
	}
}
