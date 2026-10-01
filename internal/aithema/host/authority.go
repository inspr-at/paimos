// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/aithema/journal"
	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/intake"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type authorization struct {
	Tenant       string `json:"tid"`
	Project      string `json:"pid"`
	Session      string `json:"sid"`
	Participants []struct {
		Ref  string `json:"participant_ref"`
		Role string `json:"role"`
	} `json:"participants"`
	Withdrawn *string `json:"withdrawn_at"`
}

func owner(a authorization) string {
	id := ""
	for _, p := range a.Participants {
		if p.Role == "owner" {
			if id != "" {
				return ""
			}
			id = p.Ref
		}
	}
	return id
}
func (m *Module) live(ctx context.Context, tx pgx.Tx, tid string, state journal.AuthorityState, permission string) (authorization, Settings, error) {
	var a authorization
	if json.Unmarshal(state.Authorization, &a) != nil || a.Tenant != tid || !uuidRE.MatchString(a.Project) || !uuidRE.MatchString(owner(a)) || !uuidRE.MatchString(state.PluginPrincipal) {
		return a, Settings{}, fail(403, "forbidden")
	}
	s, _, err := loadSettings(ctx, tx, tid, false)
	if err != nil {
		return a, s, err
	}
	var enabled bool
	var permissions []string
	var digest string
	err = tx.QueryRow(ctx, `SELECT enabled,permissions,manifest_digest_sha256 FROM plugin_installations WHERE tenant_id=$1 AND plugin_id='aithema'`, tid).Scan(&enabled, &permissions, &digest)
	p, _ := Plugin()
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (!enabled || digest != p.Manifest.DigestSHA256 || s.PluginPrincipal != state.PluginPrincipal || !contains(permissions, permission)) {
		return a, s, fail(409, "revoked")
	}
	if err != nil {
		return a, s, err
	}
	for _, p := range []tenant.Principal{
		{ID: owner(a), TenantID: tid, Kind: tenant.Person},
		{ID: state.PluginPrincipal, TenantID: tid, Kind: tenant.Agent, Scopes: permissions, KeyCreatorID: owner(a)},
	} {
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: a.Project}); err != nil {
			return a, s, err
		}
	}
	return a, s, nil
}
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func (m *Module) CheckAuthority(ctx context.Context, tx pgx.Tx, state journal.AuthorityState) (journal.AuthorityState, error) {
	var a authorization
	if json.Unmarshal(state.Authorization, &a) != nil {
		return state, &journal.Fault{Status: 503, Code: "unavailable"}
	}
	_, _, err := m.live(ctx, tx, a.Tenant, state, "intake.write")
	var f *Fault
	if errors.Is(err, authz.ErrForbidden) || errors.As(err, &f) && f.Code == "revoked" {
		state.Tombstone = true
		return state, nil
	}
	if err != nil {
		return state, &journal.Fault{Status: 503, Code: "unavailable"}
	}
	return state, nil
}

func (m *Module) CheckJournal(ctx context.Context, tx pgx.Tx, c tokens.Claims, capability string, state journal.AuthorityState) error {
	permission := "intake.write"
	if capability == "aithema.journal.read" {
		permission = "intake.read"
	}
	a, _, err := m.live(ctx, tx, c.TenantID, state, permission)
	if err != nil {
		status, code := 503, "unavailable"
		var f *Fault
		if errors.As(err, &f) {
			status, code = f.Status, f.Code
		}
		if errors.Is(err, authz.ErrForbidden) {
			status, code = 403, "forbidden"
		}
		return &journal.Fault{Status: status, Code: code}
	}
	if c.Actor == nil || owner(a) != c.Actor.Subject {
		return &journal.Fault{Status: 403, Code: "forbidden"}
	}
	return nil
}

// LockAuthority adapts P03 to P02 without creating a persistent permission
// grant. Both actor and plugin bindings are checked again in the effect's tx.
func (m *Module) LockAuthority(ctx context.Context, tx pgx.Tx, tid, sid string) (intake.SessionAuthority, error) {
	state, err := m.Journal.LockAuthority(ctx, tx, tid, sid)
	var f *journal.Fault
	if errors.As(err, &f) && f.Status == 404 {
		return intake.SessionAuthority{}, intake.ErrAuthorityNotFound
	}
	if err != nil {
		return intake.SessionAuthority{}, err
	}
	var a authorization
	if json.Unmarshal(state.Authorization, &a) != nil {
		return intake.SessionAuthority{}, fail(503, "unavailable")
	}
	out := intake.SessionAuthority{TenantID: tid, ProjectID: a.Project, SessionID: sid, PluginPrincipalID: state.PluginPrincipal, RequesterPrincipalID: owner(a), Generation: state.Generation, AuthEpoch: state.Epoch, Revoked: state.Tombstone || state.Suspended || a.Withdrawn != nil}
	if _, _, err := m.live(ctx, tx, tid, state, "intake.read"); err != nil {
		var f *Fault
		if !errors.Is(err, authz.ErrForbidden) && !(errors.As(err, &f) && f.Code == "revoked") {
			return out, err
		}
		out.Revoked = true
		return out, nil
	}
	_, _, writeErr := m.live(ctx, tx, tid, state, "intake.write")
	if writeErr == nil && !out.Revoked {
		out.Grant = &intake.EphemeralGrant{TenantID: tid, ProjectID: a.Project, SessionID: sid, PluginPrincipalID: state.PluginPrincipal, RequesterPrincipalID: owner(a), Generation: state.Generation, AuthEpoch: state.Epoch, ExpiresAt: m.clock().Add(time.Minute)}
	}
	if writeErr != nil {
		var f *Fault
		if !errors.Is(writeErr, authz.ErrForbidden) && !(errors.As(writeErr, &f) && f.Code == "revoked") {
			return out, writeErr
		}
	}
	return out, nil
}

// DelegatedIntake is the exact-route handoff called by auth before cookie/key
// authentication. It does not change route permissions or public declarations.
// P02 repeats verification and locks this authority during its actual effect.
func (m *Module) DelegatedIntake(w http.ResponseWriter, r *http.Request, next http.Handler) bool {
	permission := ""
	switch r.Pattern {
	case "GET /api/projects/{projectId}/intake":
		permission = "intake.read"
	case "POST /api/projects/{projectId}/intake/sources", "POST /api/projects/{projectId}/intake/transcript-turns", "POST /api/projects/{projectId}/intake/drafts", "POST /api/projects/{projectId}/intake/drafts/{draftId}/replace":
		permission = "intake.write"
	case "POST /api/projects/{projectId}/intake/drafts/{draftId}/accept":
		permission = "person-only"
	default:
		return false
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return false
	}
	scheme, raw, ok := strings.Cut(header, " ")
	raw = strings.TrimSpace(raw)
	if ok && strings.EqualFold(scheme, "Bearer") && strings.HasPrefix(raw, "aeon_") {
		return false
	}
	if permission == "person-only" {
		writeError(w, fail(403, "forbidden"))
		return true
	}
	if !ok || !strings.EqualFold(scheme, "Bearer") || len(r.Header.Values("Authorization")) != 1 || m.Keys == nil {
		writeError(w, fail(401, "unauthorized"))
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	c, err := m.Keys.VerifyDelegated(ctx, raw)
	if err != nil {
		writeError(w, fail(401, "unauthorized"))
		return true
	}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 || c.ProjectID != parts[3] || !contains(c.Capabilities, permission) || c.Actor == nil || !uuidRE.MatchString(c.Subject) || !uuidRE.MatchString(c.TenantID) {
		writeError(w, fail(403, "forbidden"))
		return true
	}
	p := tenant.Principal{ID: c.Subject, TenantID: c.TenantID, Kind: tenant.Agent, Scopes: c.Capabilities, KeyCreatorID: c.Actor.Subject}
	ctx = tenant.WithPrincipal(ctx, p)
	err = db.InTenant(ctx, m.Pool, c.TenantID, func(tx pgx.Tx) error {
		state, err := m.Journal.LockAuthority(ctx, tx, c.TenantID, c.SessionID)
		if err != nil {
			return err
		}
		a, _, err := m.live(ctx, tx, c.TenantID, state, permission)
		if err != nil {
			return err
		}
		if a.Project != c.ProjectID || state.PluginPrincipal != c.Subject || owner(a) != c.Actor.Subject {
			return fail(403, "forbidden")
		}
		if state.Tombstone || state.Suspended || a.Withdrawn != nil || state.Epoch != c.AuthEpoch {
			return fail(409, "revoked")
		}
		if permission == "intake.write" && state.Generation != c.Generation {
			return fail(409, "fenced_generation")
		}
		return nil
	})
	if err != nil {
		writeError(w, err)
		return true
	}
	next.ServeHTTP(w, r.WithContext(ctx))
	return true
}
