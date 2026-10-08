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
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Per-person UI preferences: GET and PUT /api/preferences/{key}. A value is a
// small JSON object (at most maxPreferenceBytes) that belongs to the calling
// principal only; agents.working follows the person's canonical identity and
// linked aliases. Plan saves append a value-free notification after all writes;
// ordinary UI preferences remain outside the domain event log.
// A key that was never written reads as
// {"key": ..., "value": null} so first use needs no special case.
const maxPreferenceBytes = 16 << 10

var preferenceKey = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,127}$`)
var errAgentsPlanChanged = errors.New("agents plan changed")

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
		if key == agentplan.PreferenceKey {
			if p.Kind != tenant.Person {
				return authz.ErrForbidden
			}
			owner, err := agentsPlanOwner(r.Context(), tx, p.TenantID, p.ID)
			if err != nil {
				return err
			}
			value, at, err := readAgentsPlanPreference(r.Context(), tx, p.TenantID, owner)
			if at != nil {
				out.Value, out.UpdatedAt = value, at
			}
			return err
		}
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
	if errors.Is(err, authz.ErrForbidden) {
		httpapi.WriteError(w, http.StatusForbidden, "active person required for plan")
		return
	}
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
	r.Body = http.MaxBytesReader(w, r.Body, maxPreferenceBytes+192)
	var in struct {
		Value             json.RawMessage `json:"value"`
		ExpectedUpdatedAt json.RawMessage `json:"expected_updated_at"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	var expectedAt *time.Time
	if len(in.ExpectedUpdatedAt) > 0 {
		if key != agentplan.PreferenceKey || json.Unmarshal(in.ExpectedUpdatedAt, &expectedAt) != nil {
			httpapi.WriteError(w, http.StatusBadRequest, "expected_updated_at requires an agents plan timestamp or null")
			return
		}
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
		owner := p.ID
		if key == agentplan.PreferenceKey {
			// Tenant first: serialize with access/identity changes and all plan
			// writers, including aliases and unconditional legacy clients.
			var locked string
			if err := tx.QueryRow(r.Context(), `SELECT id::text FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, p.TenantID).Scan(&locked); err != nil {
				return err
			}
			var err error
			owner, err = agentsPlanOwner(r.Context(), tx, p.TenantID, p.ID)
			if err != nil {
				return err
			}
			if err := authz.RequireTx(r.Context(), tx, p, "views.write", authz.Scope{}); err != nil {
				return err
			}
			if len(in.ExpectedUpdatedAt) > 0 {
				_, currentAt, err := readAgentsPlanPreference(r.Context(), tx, p.TenantID, owner)
				if err != nil {
					return err
				}
				if (expectedAt == nil) != (currentAt == nil) || expectedAt != nil && !expectedAt.Equal(*currentAt) {
					return errAgentsPlanChanged
				}
			}
		}
		var value []byte
		var at time.Time
		err := tx.QueryRow(r.Context(), `
			INSERT INTO user_preferences (tenant_id, principal_id, key, value)
			VALUES ($1::uuid, $2::uuid, $3, $4::jsonb)
			ON CONFLICT (tenant_id, principal_id, key) DO UPDATE SET value = EXCLUDED.value,
			updated_at = CASE WHEN EXCLUDED.key='agents.working'
				THEN greatest(clock_timestamp(), user_preferences.updated_at + interval '1 microsecond') ELSE now() END
			RETURNING value, updated_at`, p.TenantID, owner, key, string(trimmed)).Scan(&value, &at)
		if err != nil {
			return err
		}
		if key == agentplan.PreferenceKey {
			// The explicit save supersedes old alias copies. The canonical upsert
			// serializes competing saves before reconciling those existing rows.
			_, err = tx.Exec(r.Context(), `UPDATE user_preferences pref SET value=$4::jsonb,updated_at=$5
				FROM principals alias WHERE alias.tenant_id=$1::uuid AND alias.linked_to=$2::uuid
				AND pref.tenant_id=alias.tenant_id AND pref.principal_id=alias.id AND pref.key=$3`,
				p.TenantID, owner, key, string(trimmed), at)
		}
		out.Value, out.UpdatedAt = value, &at
		if err == nil && key == agentplan.PreferenceKey {
			// Last: the event counter follows all preference/alias row writes.
			err = m.eventSink.Append(r.Context(), tx, p.ID, "agents_plan.changed", nil,
				map[string]any{"principal_id": owner, "updated_at": at})
		}
		return err
	})
	if errors.Is(err, errAgentsPlanChanged) {
		httpapi.WriteError(w, http.StatusConflict, "agents plan changed; re-read before saving")
		return
	}
	if errors.Is(err, authz.ErrForbidden) {
		httpapi.WriteError(w, http.StatusForbidden, "active person required for plan")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "database operation failed")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}
