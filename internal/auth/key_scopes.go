// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

type keyScopeDelta struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

type keyScopeView struct {
	Key       agentKeyJSON `json:"key"`
	Grantable []string     `json:"grantable_scopes"`
}

var errKeyInactive = errors.New("key is revoked or expired")
var errKeyScopes = errors.New("invalid scopes")

func (m *Module) handleAgentKeyScopes(w http.ResponseWriter, r *http.Request) {
	p, ok := m.requireKeyManagement(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeBadRequest(w, "invalid id")
		return
	}
	var delta *keyScopeDelta
	if r.Method == http.MethodPatch {
		delta = &keyScopeDelta{}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(delta); err != nil {
			writeBadRequest(w, "invalid JSON")
			return
		}
		var extra any
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			writeBadRequest(w, "expected one JSON object")
			return
		}
		if delta.Add == nil && delta.Remove == nil {
			writeBadRequest(w, "add or remove is required")
			return
		}
		var err error
		delta.Add, err = cleanScopes(delta.Add)
		if err != nil {
			writeBadRequest(w, "invalid scopes")
			return
		}
		// Removals may clean up retired registry entries, but are still bounded.
		if len(delta.Remove) > 32 {
			writeBadRequest(w, "invalid scopes")
			return
		}
		for i, scope := range delta.Remove {
			scope = strings.ReplaceAll(strings.TrimSpace(scope), ":", ".")
			if scope == "" || len(scope) > 128 || strings.ContainsAny(scope, " \t\r\n\x00") || slices.Contains(delta.Add, scope) {
				writeBadRequest(w, "invalid or overlapping scopes")
				return
			}
			delta.Remove[i] = scope
		}
	}
	view, err := m.agentKeyScopes(r.Context(), p, id, delta)
	switch {
	case errors.Is(err, authz.ErrForbidden):
		writeForbidden(w)
	case errors.Is(err, errNotFound):
		writeJSON(w, http.StatusNotFound, errorJSON{Error: "key not found"})
	case errors.Is(err, errKeyInactive):
		writeJSON(w, http.StatusConflict, errorJSON{Error: errKeyInactive.Error()})
	case errors.Is(err, errKeyScopes):
		writeBadRequest(w, "invalid scopes or more than 32 scopes")
	case err != nil:
		writeInternal(w)
	case delta != nil:
		writeJSON(w, http.StatusOK, view.Key)
	default:
		writeJSON(w, http.StatusOK, view)
	}
}

func (m *Module) agentKeyScopes(ctx context.Context, p tenant.Principal, id string, delta *keyScopeDelta) (keyScopeView, error) {
	view := keyScopeView{Grantable: []string{}}
	if p.Kind != tenant.Person || p.ID == "" {
		return view, authz.ErrForbidden
	}
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Match role management and rotation: tenant first, then key. Ceiling
		// checks, deltas and audit are serialized with concurrent role edits.
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR UPDATE`, p.TenantID).Scan(&locked); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "keys.manage", authz.Scope{}); err != nil {
			return err
		}
		key, err := lockAgentKey(ctx, tx, id)
		if err != nil {
			return err
		}
		if key.RevokedAt != nil || key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now()) {
			return errKeyInactive
		}
		var creator *string
		if err := tx.QueryRow(ctx, `SELECT created_by_principal_id::text FROM agent_keys WHERE id=$1::uuid`, id).Scan(&creator); err != nil {
			return err
		}
		agent := tenant.Principal{ID: key.PrincipalID, TenantID: p.TenantID, Kind: tenant.Agent}
		if creator != nil {
			agent.KeyCreatorID = *creator
		}
		ceiling, err := authz.EffectiveTx(ctx, tx, agent, "")
		if err != nil {
			return err
		}
		editor, err := authz.EffectiveTx(ctx, tx, p, "")
		if err != nil {
			return err
		}
		for _, scope := range ceiling.Workspace.Permissions {
			perm, known := authz.Lookup(scope)
			if known && perm.AgentGrantable && slices.Contains(editor.Workspace.Permissions, scope) {
				view.Grantable = append(view.Grantable, scope)
			}
		}
		if delta != nil {
			after := []string{}
			for _, scope := range key.Scopes {
				scope = strings.ReplaceAll(scope, ":", ".")
				if !slices.Contains(delta.Remove, scope) && !slices.Contains(after, scope) {
					after = append(after, scope)
				}
			}
			for _, scope := range delta.Add {
				if !slices.Contains(after, scope) {
					after = append(after, scope)
				}
			}
			if len(after) > 32 {
				return errKeyScopes
			}
			for _, scope := range after {
				if !slices.Contains(view.Grantable, scope) {
					return authz.ErrForbidden
				}
			}
			if !slices.Equal(key.Scopes, after) {
				before := keySnapshot(key)
				if _, err := tx.Exec(ctx, `UPDATE agent_keys SET scopes=$2::text[] WHERE id=$1::uuid`, id, after); err != nil {
					return err
				}
				key.Scopes = after
				if _, err := events.Append(ctx, tx, p, events.Change{Type: "agent_key.scopes_changed", Before: before, After: keySnapshot(key)}); err != nil {
					return err
				}
			}
		}
		view.Key = keyJSON(key)
		return nil
	})
	return view, err
}
