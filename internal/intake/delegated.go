// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/tenant"
)

// SessionAuthority is host-owned session state, never supplied by the caller.
// Live wiring to the P03 store belongs to AEON-P04. Revoked includes withdrawn
// authority, suspend/purge tombstones, disabled tenants and removed plugins.
type SessionAuthority struct {
	TenantID, ProjectID, SessionID, PluginPrincipalID, RequesterPrincipalID string
	Generation, AuthEpoch                                                   int64
	Revoked                                                                 bool
	Grant                                                                   *EphemeralGrant
}

// EphemeralGrant is a narrow, temporary LiveGrant. It does not create a role,
// agent key, approval decision or persistent agent_permission_grants row.
type EphemeralGrant struct {
	TenantID, ProjectID, SessionID, PluginPrincipalID, RequesterPrincipalID string
	Generation, AuthEpoch                                                   int64
	ExpiresAt                                                               time.Time
}

// AuthorityStore must lock current session authority in tx until its commit or
// rollback. Takeover/revocation must acquire the same lock. This makes the
// authority check and intake effect atomic, including time spent waiting for
// the project lock. ErrAuthorityNotFound represents unknown/foreign sessions.
type AuthorityStore interface {
	LockAuthority(context.Context, pgx.Tx, string, string) (SessionAuthority, error)
}

var ErrAuthorityNotFound = errors.New("intake session authority not found")

// NewDelegated prepares the P02 boundary for P04's host auth/plugin wiring.
// Mount wraps only intake routes; it grants no global JWT access. The outer
// auth boundary must still enforce current project bindings for both principals.
// Missing keys/store fails closed. New preserves existing person/key behavior.
func NewDelegated(pool *pgxpool.Pool, keys *tokens.KeySet, store AuthorityStore) *Module {
	return &Module{pool: pool, keys: keys, authority: store, clock: time.Now}
}

type delegatedKey struct{}

func delegatedClaims(ctx context.Context) (tokens.Claims, bool) {
	c, ok := ctx.Value(delegatedKey{}).(tokens.Claims)
	return c, ok
}

func (m *Module) delegated(next http.HandlerFunc, capability string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		if header == "" {
			next(w, r)
			return
		}
		scheme, raw, ok := strings.Cut(header, " ")
		raw = strings.TrimSpace(raw)
		// A bearer credential never authorizes person-only acceptance, even
		// when the caller also presents a valid person session cookie.
		if capability == "" {
			writeError(w, http.StatusForbidden, "only a person session may accept a draft")
			return
		}
		if ok && strings.EqualFold(scheme, "Bearer") && strings.HasPrefix(raw, "aeon_") {
			next(w, r)
			return
		}
		if !ok || !strings.EqualFold(scheme, "Bearer") || raw == "" || m.keys == nil || m.authority == nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		c, err := m.keys.VerifyDelegated(r.Context(), raw)
		if err != nil {
			if errors.Is(err, tokens.ErrUnavailable) {
				writeError(w, http.StatusServiceUnavailable, "authority unavailable")
			} else {
				writeError(w, http.StatusUnauthorized, "invalid delegated token")
			}
			return
		}
		if !contains(c.Capabilities, capability) || c.ProjectID != strings.ToLower(r.PathValue("projectId")) || c.Actor == nil ||
			!uuidPattern.MatchString(c.TenantID) || !uuidPattern.MatchString(c.Subject) || !uuidPattern.MatchString(c.Actor.Subject) {
			writeError(w, http.StatusForbidden, "delegated scope required")
			return
		}
		p := tenant.Principal{ID: c.Subject, TenantID: c.TenantID, Kind: tenant.Agent, Scopes: c.Capabilities, KeyCreatorID: c.Actor.Subject}
		if current, exists := tenant.PrincipalFrom(r.Context()); exists && (current.ID != p.ID || current.TenantID != p.TenantID || current.Kind != p.Kind) {
			writeError(w, http.StatusForbidden, "foreign principal")
			return
		}
		ctx = tenant.WithPrincipal(r.Context(), p)
		ctx = context.WithValue(ctx, delegatedKey{}, c)
		next(w, r.WithContext(ctx))
	}
}

func checkAuthority(c tokens.Claims, a SessionAuthority, write bool, now time.Time) error {
	if c.Actor == nil || a.TenantID != c.TenantID || a.ProjectID != c.ProjectID || a.SessionID != c.SessionID ||
		a.PluginPrincipalID != c.Subject || a.RequesterPrincipalID != c.Actor.Subject {
		return fail(http.StatusForbidden, "foreign session scope")
	}
	if a.Revoked || a.AuthEpoch != c.AuthEpoch {
		return refusal(http.StatusConflict, "revoked", "session authority revoked")
	}
	if !write {
		return nil // Read routes deliberately do not fence generation.
	}
	if a.Generation != c.Generation {
		return refusal(http.StatusConflict, "fenced_generation", "worker generation fenced")
	}
	g := a.Grant
	if g == nil || g.TenantID != c.TenantID || g.ProjectID != c.ProjectID || g.SessionID != c.SessionID ||
		g.PluginPrincipalID != c.Subject || g.RequesterPrincipalID != c.Actor.Subject || g.Generation != c.Generation ||
		g.AuthEpoch != c.AuthEpoch || !g.ExpiresAt.After(now) {
		return fail(http.StatusForbidden, "current ephemeral grant required: "+scopeWrite)
	}
	return nil
}

func (m *Module) intakeTx(r *http.Request, p tenant.Principal, projectID string, write bool, fn func(pgx.Tx, []string) error) error {
	return m.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		scopes, err := authorize(r.Context(), r, tx, p, write)
		if err != nil {
			return err
		}
		if write {
			err = lockProject(r.Context(), tx, projectID)
		} else {
			err = projectVisible(r.Context(), tx, projectID)
		}
		if err != nil {
			return err
		}
		if c, delegated := delegatedClaims(r.Context()); delegated {
			// Reuse P01's verification at time of use: waiting for a project
			// lock must not turn an expired credential into a valid write.
			_, raw, _ := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
			if _, err := m.keys.VerifyDelegated(r.Context(), strings.TrimSpace(raw)); err != nil {
				if errors.Is(err, tokens.ErrUnavailable) {
					return fail(http.StatusServiceUnavailable, "authority unavailable")
				}
				return fail(http.StatusUnauthorized, "invalid delegated token")
			}
			a, err := m.authority.LockAuthority(r.Context(), tx, c.TenantID, c.SessionID)
			if errors.Is(err, ErrAuthorityNotFound) {
				return fail(http.StatusForbidden, "foreign session scope")
			}
			if err != nil {
				return refusal(http.StatusServiceUnavailable, "unavailable", "authority unavailable")
			}
			if err := checkAuthority(c, a, write, m.clock()); err != nil {
				return err
			}
		}
		return fn(tx, scopes)
	})
}

func bindRequester(ctx context.Context, in *draftWrite) error {
	c, delegated := delegatedClaims(ctx)
	if !delegated {
		if in.RequesterPrincipalID != nil {
			return fail(http.StatusForbidden, "delegated requester required")
		}
		return nil
	}
	if in.RequesterPrincipalID != nil && *in.RequesterPrincipalID != c.Actor.Subject {
		return fail(http.StatusForbidden, "foreign requester")
	}
	requester := c.Actor.Subject
	in.RequesterPrincipalID = &requester
	return nil
}
