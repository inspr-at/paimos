// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
)

var uuidRe = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type principalJSON struct {
	ID       string   `json:"id"`
	TenantID string   `json:"tenant_id"`
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Email    *string  `json:"email"`
	Roles    []string `json:"roles"`
}

type tenantJSON struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type identityJSON struct {
	ID          string  `json:"id"`
	Issuer      string  `json:"issuer"`
	Subject     string  `json:"subject"`
	Email       *string `json:"email"`
	DisplayName *string `json:"display_name"`
}

type meJSON struct {
	DevMode   bool          `json:"dev_mode"`
	Principal principalJSON `json:"principal"`
	Tenant    tenantJSON    `json:"tenant"`
	Identity  *identityJSON `json:"identity"`
}

type agentKeyJSON struct {
	ID          string     `json:"id"`
	PrincipalID string     `json:"principal_id"`
	Name        string     `json:"name"`
	Prefix      string     `json:"prefix"`
	Scopes      []string   `json:"scopes"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
}

type agentKeyCreatedJSON struct {
	agentKeyJSON
	Token string `json:"token"`
}

func (m *Module) meJSONFrom(v meView) meJSON {
	roles := v.Principal.Roles
	if roles == nil {
		roles = []string{}
	}
	out := meJSON{
		DevMode: m.cfg.Dev(),
		Principal: principalJSON{
			ID:       v.Principal.ID,
			TenantID: v.Principal.TenantID,
			Kind:     string(v.Principal.Kind),
			Name:     v.Principal.Name,
			Email:    v.Email,
			Roles:    roles,
		},
		Tenant: tenantJSON{ID: v.TenantID, Slug: v.Slug, Name: v.Name},
	}
	if v.Identity != nil {
		out.Identity = &identityJSON{
			ID:          v.Identity.ID,
			Issuer:      v.Identity.Issuer,
			Subject:     v.Identity.Subject,
			Email:       v.Identity.Email,
			DisplayName: v.Identity.DisplayName,
		}
	}
	return out
}

func keyJSON(rec keyRecord) agentKeyJSON {
	scopes := rec.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return agentKeyJSON{
		ID:          rec.ID,
		PrincipalID: rec.PrincipalID,
		Name:        rec.Name,
		Prefix:      rec.Prefix,
		Scopes:      scopes,
		CreatedAt:   rec.CreatedAt,
		ExpiresAt:   rec.ExpiresAt,
		LastUsedAt:  rec.LastUsedAt,
		RevokedAt:   rec.RevokedAt,
	}
}

func (m *Module) requireKeyManagement(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w)
		return tenant.Principal{}, false
	}
	if p.Kind != tenant.Person || authz.Require(authz.BindPool(r.Context(), m.pool), "keys.manage", authz.Scope{}) != nil {
		writeForbidden(w)
		return tenant.Principal{}, false
	}
	return p, true
}

func (m *Module) handleCreateAgentKey(w http.ResponseWriter, r *http.Request) {
	p, ok := m.requireKeyManagement(w, r)
	if !ok {
		return
	}
	var body struct {
		RotateKeyID *string    `json:"rotate_key_id"`
		Name        string     `json:"name"`
		PrincipalID string     `json:"principal_id"`
		Scopes      []string   `json:"scopes"`
		ExpiresAt   *time.Time `json:"expires_at"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.ExpiresAt != nil && !body.ExpiresAt.After(time.Now()) {
		writeBadRequest(w, "expires_at must be in the future")
		return
	}
	if body.RotateKeyID != nil {
		if !uuidRe.MatchString(*body.RotateKeyID) || body.Name != "" || body.PrincipalID != "" || body.Scopes != nil {
			writeBadRequest(w, "rotation requires rotate_key_id and optional expires_at only")
			return
		}
		rec, err := m.rotateAgentKey(r.Context(), p, *body.RotateKeyID, body.ExpiresAt)
		m.writeCreatedAgentKey(w, rec, err)
		return
	}
	name := strings.TrimSpace(body.Name)
	principalID := strings.TrimSpace(body.PrincipalID)
	if principalID != "" && !uuidRe.MatchString(principalID) {
		writeBadRequest(w, "principal_id must be a UUID")
		return
	}
	if (principalID == "" && name == "") || utf8.RuneCountInString(name) > 200 || strings.ContainsRune(name, 0) {
		writeBadRequest(w, "name is required")
		return
	}
	scopes, err := cleanScopes(body.Scopes)
	if err != nil {
		writeBadRequest(w, "invalid scopes")
		return
	}
	rec, err := m.createAgentKey(r.Context(), p, name, principalID, scopes, body.ExpiresAt)
	m.writeCreatedAgentKey(w, rec, err)
}

func (m *Module) writeCreatedAgentKey(w http.ResponseWriter, rec keyRecord, err error) {
	if err != nil {
		if errors.Is(err, errKeyRevoked) {
			writeJSON(w, http.StatusConflict, errorJSON{Error: "key already revoked or rotated"})
			return
		}
		if errors.Is(err, errNotFound) {
			writeJSON(w, http.StatusNotFound, errorJSON{Error: "agent or key not found"})
			return
		}
		if errors.Is(err, errNotAgent) {
			writeBadRequest(w, "principal_id must be an agent")
			return
		}
		if errors.Is(err, errServicePrincipal) || errors.Is(err, authz.ErrForbidden) {
			writeForbidden(w)
			return
		}
		writeInternal(w)
		return
	}
	created := agentKeyCreatedJSON{agentKeyJSON: keyJSON(rec), Token: rec.Token}
	writeJSON(w, http.StatusCreated, created)
}

func (m *Module) handleListAgentKeys(w http.ResponseWriter, r *http.Request) {
	p, ok := m.requireKeyManagement(w, r)
	if !ok {
		return
	}
	recs, err := m.listAgentKeys(r.Context(), p.TenantID)
	if err != nil {
		writeInternal(w)
		return
	}
	out := make([]agentKeyJSON, 0, len(recs))
	for _, rec := range recs {
		out = append(out, keyJSON(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (m *Module) handleRevokeAgentKey(w http.ResponseWriter, r *http.Request) {
	p, ok := m.requireKeyManagement(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeBadRequest(w, "invalid id")
		return
	}
	err := m.revokeAgentKey(r.Context(), p, id)
	if errors.Is(err, errNotFound) {
		writeJSON(w, http.StatusNotFound, errorJSON{Error: "not found"})
		return
	}
	if err != nil {
		writeInternal(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func cleanScopes(in []string) ([]string, error) {
	if len(in) > 32 {
		return nil, errors.New("too many scopes")
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || len(s) > 128 || strings.ContainsAny(s, " \t\r\n") {
			return nil, errors.New("bad scope")
		}
		key := strings.ReplaceAll(s, ":", ".")
		perm, ok := authz.Lookup(key)
		if !ok || !perm.AgentGrantable {
			return nil, errors.New("unknown scope")
		}
		if slices.Contains(out, key) {
			continue
		}
		out = append(out, key)
	}
	return out, nil
}

// NormalizeScopes converts colon scopes to dot notation and rejects unknown
// or non-agent-grantable permissions.
func NormalizeScopes(in []string) ([]string, error) { return cleanScopes(in) }
