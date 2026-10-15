// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func (m *Module) mountAdmission(mux *http.ServeMux) {
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /api/delivery/{itemId}/admission", m.admission},
		{"PUT /api/delivery/{itemId}/admission", m.admission},
		{"GET /api/projects/{projectId}/delivery-admission", m.admissionSettings},
		{"PUT /api/projects/{projectId}/delivery-admission", m.admissionSettings},
	} {
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			r = r.WithContext(ctx)
			if r.Method == http.MethodPut {
				if err := httpapi.BufferRequestBodyTimeout(w, r, 16<<10, 5*time.Second); err != nil {
					respondError(w, fail(400, "invalid admission body"))
					return
				}
			}
			route.handler(w, r)
		})
	}
}
func (m *Module) admission(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := strings.ToLower(r.PathValue("itemId"))
	if !workorders.UUID(id) {
		respondError(w, fail(400, "invalid delivery item"))
		return
	}
	write := r.Method == http.MethodPut
	var in AdmissionControl
	if write {
		if err := decode(w, r, &in); err != nil {
			respondError(w, err)
			return
		}
		if !in.Binding.Valid() || in.Revision < 0 || in.Holds == nil || !validAdmissionHolds(in.Holds) {
			respondError(w, fail(400, "invalid admission control"))
			return
		}
	}
	var out AdmissionSnapshot
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Read admission under the same tenant fence as observations and approvals,
		// so READ COMMITTED cannot combine facts from two different heads.
		if err := db.LockTree(r.Context(), tx, p.TenantID); err != nil {
			return err
		}
		item, err := load(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if item == nil {
			return pgx.ErrNoRows
		}
		permission := "delivery.read"
		if write {
			permission = "delivery.manage"
		}
		if err = authz.RequireTx(r.Context(), tx, p, permission, scope(item.Project)); err != nil {
			return err
		}
		if write && p.Kind != tenant.Person {
			return authz.ErrForbidden
		}
		out, err = AdmissionTx(r.Context(), tx, *item)
		if err != nil || !write {
			return err
		}
		if in.Revision != out.Control.Revision || in.Binding != out.Control.Binding {
			return fail(409, "admission changed; reload first")
		}
		before := out.Control
		raw, _ := json.Marshal(in.Holds)
		_, err = tx.Exec(r.Context(), `UPDATE delivery_admission_bindings SET holds=$3,fix=$4,control_revision=control_revision+1 WHERE repository=$1 AND pull_request=$2`, item.Repository, *item.PR, raw, in.Fix)
		if err != nil {
			return err
		}
		out, err = AdmissionTx(r.Context(), tx, *item)
		if err != nil {
			return err
		}
		_, err = events.Append(r.Context(), tx, p, events.Change{NodeID: item.Ticket, Type: "delivery.admission.control", Before: before, After: out.Control})
		return err
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) admissionSettings(w http.ResponseWriter, r *http.Request) {
	write := r.Method == http.MethodPut
	permission := "delivery.read"
	var in AdmissionPolicy
	if write {
		permission = "delivery.manage"
		if err := decode(w, r, &in); err != nil {
			respondError(w, err)
			return
		}
		if in.Revision < 0 || in.Holds == nil || !validAdmissionHolds(in.Holds) || in.NightlyState != "unknown" && in.NightlyState != "red" && in.NightlyState != "green" {
			respondError(w, fail(400, "invalid admission settings"))
			return
		}
	}
	// reviewRequest acquires tenant -> tree before the live RequireTx.
	m.reviewRequest(w, r, permission, true, func(tx pgx.Tx, p tenant.Principal, project string) (any, int, error) {
		before, err := admissionPolicyTx(r.Context(), tx, project)
		if err != nil || !write {
			return before, 200, err
		}
		if p.Kind != tenant.Person {
			return nil, 200, authz.ErrForbidden
		}
		if in.Revision != before.Revision {
			return nil, 200, fail(409, "admission settings changed; reload first")
		}
		in.Revision++
		raw, _ := json.Marshal(in.Holds)
		_, err = tx.Exec(r.Context(), `INSERT INTO delivery_admission_settings(tenant_id,project_id,revision,block_on_nightly_red,nightly_state,holds) VALUES($1,$2,$3,$4,$5,$6)
 ON CONFLICT(tenant_id,project_id) DO UPDATE SET revision=EXCLUDED.revision,block_on_nightly_red=EXCLUDED.block_on_nightly_red,nightly_state=EXCLUDED.nightly_state,holds=EXCLUDED.holds`, p.TenantID, project, in.Revision, in.BlockOnNightlyRed, in.NightlyState, raw)
		if err == nil {
			_, err = events.Append(r.Context(), tx, p, events.Change{NodeID: &project, Type: "delivery.admission.settings", Before: before, After: in})
		}
		return in, 200, err
	})
}
