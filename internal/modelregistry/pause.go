// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type modelPause struct {
	Until  *time.Time `json:"until"`
	Reason string     `json:"reason"`
	By     *string    `json:"by"`
	At     time.Time  `json:"at"`
}

type modelPauseResult struct {
	Harness string      `json:"harness"`
	Model   string      `json:"model"`
	Pause   *modelPause `json:"pause"`
}

func (m *Module) writeModelPause(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writePreferenceError(w, prefFail(403, "person_required"))
		return
	}
	harness, model, err := linePath(r)
	if err != nil {
		writeBoardError(w, err)
		return
	}
	var in struct {
		Until  json.RawMessage `json:"until"`
		Reason string          `json:"reason"`
	}
	var until *time.Time
	if r.Method != http.MethodDelete {
		if err := workBody(w, r); err != nil {
			writePreferenceError(w, err)
			return
		}
		if err := decodeJSON(w, r, &in); err != nil {
			writePreferenceError(w, err)
			return
		}
		in.Reason = strings.TrimSpace(in.Reason)
		if len(in.Until) == 0 || json.Unmarshal(in.Until, &until) != nil || in.Reason == "" || !boundedText(in.Reason, 1000) || strings.ContainsRune(in.Reason, '\x00') {
			writePreferenceError(w, prefFail(422, "invalid_pause"))
			return
		}
	}
	out := modelPauseResult{Harness: harness, Model: model}
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := preferenceFence(ctx, tx, p); err != nil {
			return err
		}
		if err := workorders.RefreshPrincipal(ctx, tx, p); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "models.manage", authz.Scope{}); err != nil {
			return err
		}
		if err := catalogLock(ctx, tx); err != nil {
			return err
		}
		clock := m.validationClock
		if clock == nil {
			clock = validationNow
		}
		now, err := clock(ctx, tx)
		if err != nil {
			return err
		}
		if until != nil && !now.Before(*until) {
			return prefFail(422, "pause_until_must_be_future")
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE harness=$1 AND model=$2 LIMIT 1)`, harness, model).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return prefFail(404, "unknown_model_route")
		}
		var before modelPause
		var prior *modelPause
		err = tx.QueryRow(ctx, `SELECT until_at,reason,set_by::text,set_at FROM model_pauses WHERE harness=$1 AND model=$2`, harness, model).Scan(&before.Until, &before.Reason, &before.By, &before.At)
		if err == nil {
			prior = &before
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if r.Method == http.MethodDelete {
			if _, err := tx.Exec(ctx, `DELETE FROM model_pauses WHERE harness=$1 AND model=$2`, harness, model); err != nil {
				return err
			}
		} else {
			if prior == nil {
				var count int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM model_pauses LIMIT 513) pauses`).Scan(&count); err != nil {
					return err
				}
				if count >= 512 {
					return prefFail(413, "model_pause_limit")
				}
			}
			out.Pause = &modelPause{}
			if err := tx.QueryRow(ctx, `INSERT INTO model_pauses(tenant_id,harness,model,until_at,reason,set_by) VALUES($1,$2,$3,$4,$5,$6)
 ON CONFLICT(tenant_id,harness,model) DO UPDATE SET until_at=excluded.until_at,reason=excluded.reason,set_by=excluded.set_by,set_at=now()
 RETURNING until_at,reason,set_by::text,set_at`, p.TenantID, harness, model, until, in.Reason, p.ID).Scan(&out.Pause.Until, &out.Pause.Reason, &out.Pause.By, &out.Pause.At); err != nil {
				return err
			}
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "model.pause_changed", Before: map[string]any{"harness": harness, "model": model, "pause": prior}, After: out})
		return err
	})
	if err != nil {
		writeBoardError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
