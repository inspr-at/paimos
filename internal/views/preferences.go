// SPDX-License-Identifier: AGPL-3.0-only

package views

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Per-person UI preferences: GET and PUT /api/preferences/{key}. A value is a
// small JSON object (at most maxPreferenceBytes) that belongs to the calling
// principal only; nobody else can read or write it. Preferences are not domain
// changes, so no event is appended. A key that was never written reads as
// {"key": ..., "value": null} so first use needs no special case.
const maxPreferenceBytes = 16 << 10

var preferenceKey = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,127}$`)

type preference struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	UpdatedAt *time.Time      `json:"updated_at"`
}

func (m *Module) getPreference(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	key := r.PathValue("key")
	if !preferenceKey.MatchString(key) {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid preference key")
		return
	}
	out := preference{Key: key, Value: json.RawMessage("null")}
	err := m.inTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var value []byte
		var at time.Time
		err := tx.QueryRow(r.Context(), `SELECT value, updated_at FROM user_preferences WHERE principal_id = $1::uuid AND key = $2`, p.ID, key).Scan(&value, &at)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		out.Value, out.UpdatedAt = value, &at
		return nil
	})
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "database operation failed")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) putPreference(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	key := r.PathValue("key")
	if !preferenceKey.MatchString(key) {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid preference key")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPreferenceBytes+64)
	var in struct {
		Value json.RawMessage `json:"value"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	trimmed := bytes.TrimSpace(in.Value)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		httpapi.WriteError(w, http.StatusBadRequest, "value must be a JSON object")
		return
	}
	if len(trimmed) > maxPreferenceBytes {
		httpapi.WriteError(w, http.StatusRequestEntityTooLarge, "value is too large")
		return
	}
	if key == agentplan.PreferenceKey {
		if p.Kind != tenant.Person {
			httpapi.WriteError(w, http.StatusForbidden, "only the person may change their plan")
			return
		}
		if _, _, err := agentplan.Decode(trimmed); err != nil {
			httpapi.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	out := preference{Key: key}
	err := m.inTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var value []byte
		var at time.Time
		err := tx.QueryRow(r.Context(), `
			INSERT INTO user_preferences (tenant_id, principal_id, key, value)
			VALUES ($1::uuid, $2::uuid, $3, $4::jsonb)
			ON CONFLICT (tenant_id, principal_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
			RETURNING value, updated_at`, p.TenantID, p.ID, key, string(trimmed)).Scan(&value, &at)
		out.Value, out.UpdatedAt = value, &at
		return err
	})
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "database operation failed")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}
