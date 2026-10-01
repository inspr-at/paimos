// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"net/http"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

func (m *Module) acceptProposal(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "models.manage"); err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Harness string `json:"harness"`
		Model   string `json:"model"`
		Effort  string `json:"effort"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	o := Observation{ReportID: EvidenceID("proposal"), Harness: in.Harness, Model: in.Model, Effort: in.Effort, Status: "advertised"}
	if err := validateObservations([]Observation{o}); err != nil {
		writeErr(w, err)
		return
	}
	if KnownInvalid(o.Model) {
		writeErr(w, fail(400, "known invalid model"))
		return
	}
	pin, ok := observedPin(o)
	if !ok {
		writeErr(w, fail(400, "unmapped model: create a person-managed profile with an explicit family and tier"))
		return
	}
	var out Profile
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if err := catalogLock(r.Context(), tx); err != nil {
			return err
		}
		if err := authz.RequireTx(r.Context(), tx, p, "models.manage", authz.Scope{}); err != nil {
			return fail(403, "permission denied")
		}
		var exists bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM model_observations WHERE harness=$1 AND model=$2 AND effort=$3)`, o.Harness, o.Model, o.Effort).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fail(404, "model proposal not found")
		}
		profiles, err := listProfiles(r.Context(), tx)
		if err != nil {
			return err
		}
		for _, profile := range profiles {
			if profile.Harness == o.Harness && profile.Model == o.Model && profile.Effort == o.Effort {
				if profile.Enabled {
					out = profile
					return nil
				}
				// Profiles are append-only, including their enabled state. A
				// person grant creates a new pin and preserves the observation.
				pin.Version = profile.Version + "-accepted"
			}
		}
		out, err = insertProfile(r.Context(), tx, p.TenantID, pin)
		if err != nil {
			return err
		}
		return writeEvent(r.Context(), tx, p, "model.proposal_accepted", nil, out)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
