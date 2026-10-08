// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

func (m *Module) retirement(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writePreferenceError(w, prefFail(403, "person_required"))
		return
	}
	id := r.PathValue("id")
	if !uuidRE.MatchString(id) {
		writePreferenceError(w, prefFail(400, "invalid_profile_id"))
		return
	}
	var in struct {
		Reason   string     `json:"reason"`
		RetireAt *time.Time `json:"retire_at"`
	}
	var err error
	if r.Method == http.MethodPost {
		err = decodeJSON(w, r, &in)
		in.Reason = strings.TrimSpace(in.Reason)
		if err == nil && (in.Reason == "" || !boundedText(in.Reason, 200)) {
			err = prefFail(422, "invalid_reason")
		}
	}
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	out := map[string]any{"profile_id": id, "retired": r.Method == http.MethodPost}
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := preferenceFence(ctx, tx, p); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "models.manage", authz.Scope{}); err != nil {
			return err
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		if in.RetireAt != nil && !in.RetireAt.After(now) {
			return prefFail(422, "retire_at_must_be_future")
		}
		out["retire_at"] = in.RetireAt
		out["retired"] = r.Method == http.MethodPost && in.RetireAt == nil
		var found string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM model_profiles WHERE id=$1`, id).Scan(&found); err != nil {
			if err == pgx.ErrNoRows {
				return prefFail(404, "model_profile_not_found")
			}
			return err
		}
		var before *string
		if err := tx.QueryRow(ctx, `SELECT (SELECT reason FROM model_profile_retirements WHERE profile_id=$1)`, id).Scan(&before); err != nil {
			return err
		}
		ev := "model.profile_restored"
		if r.Method == http.MethodPost {
			if before != nil {
				return prefFail(409, "already_retired")
			}
			if _, err := tx.Exec(ctx, `INSERT INTO model_profile_retirements(tenant_id,profile_id,reason,retired_by,retire_at) VALUES($1,$2,$3,$4,$5)`, p.TenantID, id, in.Reason, p.ID, in.RetireAt); err != nil {
				return err
			}
			out["reason"] = in.Reason
			ev = "model.profile_retired"
			if in.RetireAt != nil {
				ev = "model.profile_retirement_scheduled"
			}
		} else {
			if before == nil {
				return nil
			}
			if _, err := tx.Exec(ctx, `DELETE FROM model_profile_retirements WHERE profile_id=$1`, id); err != nil {
				return err
			}
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: ev, Before: map[string]any{"profile_id": id, "reason": before, "retired": before != nil}, After: out})
		return err
	})
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
