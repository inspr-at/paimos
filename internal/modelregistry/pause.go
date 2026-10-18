// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
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

// Cursor encodes Grok's native effort in model ids. The resolver groups those
// ids into one route; pause/resume writes must cover the same bounded group.
func modelPauseRoutes(ctx context.Context, tx pgx.Tx, harness, model string) ([]string, error) {
	var family string
	err := tx.QueryRow(ctx, `SELECT family FROM model_profiles WHERE harness=$1 AND model=$2 LIMIT 1`, harness, model).Scan(&family)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, prefFail(404, "unknown_model_route")
	}
	if err != nil {
		return nil, err
	}
	if harness != "cursor" || family != "xai" {
		return []string{model}, nil
	}
	wantLine, wantVersion := modelsLine(Profile{Harness: harness, Family: family, Model: model})
	rows, err := tx.Query(ctx, `SELECT DISTINCT model FROM model_profiles WHERE harness=$1 AND family=$2 ORDER BY model LIMIT 513`, harness, family)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	models, count := []string{}, 0
	for rows.Next() {
		count++
		if count > 512 {
			return nil, prefFail(413, "model_pause_limit")
		}
		var candidate string
		if err := rows.Scan(&candidate); err != nil {
			return nil, err
		}
		line, version := modelsLine(Profile{Harness: harness, Family: family, Model: candidate})
		if line == wantLine && version == wantVersion {
			if len(models) >= 32 {
				return nil, prefFail(413, "model_pause_limit")
			}
			models = append(models, candidate)
		}
	}
	return models, rows.Err()
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
		models, err := modelPauseRoutes(ctx, tx, harness, model)
		if err != nil {
			return err
		}
		prior := map[string]modelPause{}
		rows, err := tx.Query(ctx, `SELECT model,until_at,reason,set_by::text,set_at FROM model_pauses WHERE harness=$1 AND model=ANY($2::text[]) ORDER BY model LIMIT 513`, harness, models)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name string
			var pause modelPause
			if err := rows.Scan(&name, &pause.Until, &pause.Reason, &pause.By, &pause.At); err != nil {
				rows.Close()
				return err
			}
			prior[name] = pause
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if r.Method == http.MethodDelete {
			if _, err := tx.Exec(ctx, `DELETE FROM model_pauses WHERE harness=$1 AND model=ANY($2::text[])`, harness, models); err != nil {
				return err
			}
		} else {
			if len(prior) < len(models) {
				var count int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM model_pauses LIMIT 513) pauses`).Scan(&count); err != nil {
					return err
				}
				if count+len(models)-len(prior) > 512 {
					return prefFail(413, "model_pause_limit")
				}
			}
			for _, name := range models {
				pause := &modelPause{}
				if err := tx.QueryRow(ctx, `INSERT INTO model_pauses(tenant_id,harness,model,until_at,reason,set_by) VALUES($1,$2,$3,$4,$5,$6)
 ON CONFLICT(tenant_id,harness,model) DO UPDATE SET until_at=excluded.until_at,reason=excluded.reason,set_by=excluded.set_by,set_at=now()
					RETURNING until_at,reason,set_by::text,set_at`, p.TenantID, harness, name, until, in.Reason, p.ID).Scan(&pause.Until, &pause.Reason, &pause.By, &pause.At); err != nil {
					return err
				}
				if name == model {
					out.Pause = pause
				}
			}
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "model.pause_changed", Before: map[string]any{"harness": harness, "model": model, "pauses": prior}, After: map[string]any{"route": out, "models": models}})
		return err
	})
	if err != nil {
		writeBoardError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
