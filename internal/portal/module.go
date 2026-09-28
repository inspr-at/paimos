// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	zeroTenant        = "00000000-0000-0000-0000-000000000000"
	portalServiceRole = "portal_public_service"
	ballotCookie      = "aeon_portal_ballot"
)

var (
	slugPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	keyPattern    = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}-[1-9][0-9]*$`)
	uuidPattern   = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	ballotPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	errClosed     = errors.New("portal closed")
)

// Module serves the public catalog and the tenant switch that keeps it off.
type Module struct {
	pool          *pgxpool.Pool
	secureCookies bool
	limitCalls    atomic.Uint64
}

var _ httpapi.Module = (*Module)(nil)

// New returns the portal module. secureCookies is true outside local dev, matching session cookies.
func New(pool *pgxpool.Pool, secureCookies bool) *Module {
	return &Module{pool: pool, secureCookies: secureCookies}
}

func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/portal/settings", m.readSettings)
	mux.HandleFunc("PATCH /api/portal/settings", m.updateSettings)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}", m.read)
	mux.HandleFunc("POST /api/public/portal/{tenantSlug}/wishes/{wishKey}/votes", m.vote)
}

func write(w http.ResponseWriter, status int, value any) {
	httpapi.WriteJSON(w, status, value)
}

func fail(w http.ResponseWriter, status int, message string) {
	httpapi.WriteError(w, status, message)
}

func publicHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

func validKey(key string) bool {
	return len(key) <= 30 && keyPattern.MatchString(key)
}

type portalSettings struct {
	Enabled bool `json:"enabled"`
}

type settingsWrite struct {
	Enabled *bool `json:"enabled"`
}

func (m *Module) person(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.Kind != tenant.Person || !uuidPattern.MatchString(p.TenantID) || !uuidPattern.MatchString(p.ID) {
		fail(w, http.StatusUnauthorized, "sign in required")
		return tenant.Principal{}, false
	}
	return p, true
}

func decodeJSON(r *http.Request, dest any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("JSON required")
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, 1025))
	if err != nil || len(buf) > 1024 {
		return errors.New("invalid JSON")
	}
	dec := json.NewDecoder(strings.NewReader(string(buf)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return errors.New("invalid JSON")
	}
	var extra struct{}
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("invalid JSON")
	}
	return nil
}

func (m *Module) readSettings(w http.ResponseWriter, r *http.Request) {
	publicHeaders(w)
	p, ok := m.person(w, r)
	if !ok {
		return
	}
	if m.pool == nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	var enabled bool
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(r.Context(), tx, p, "settings.manage", authz.Scope{}); err != nil {
			return err
		}
		err := tx.QueryRow(r.Context(), `SELECT enabled FROM portal_settings`).Scan(&enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			enabled = false
			return nil
		}
		return err
	})
	if errors.Is(err, authz.ErrForbidden) {
		fail(w, http.StatusForbidden, "permission denied")
		return
	}
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	write(w, http.StatusOK, portalSettings{Enabled: enabled})
}

func (m *Module) updateSettings(w http.ResponseWriter, r *http.Request) {
	publicHeaders(w)
	p, ok := m.person(w, r)
	if !ok {
		return
	}
	if m.pool == nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	var in settingsWrite
	if err := decodeJSON(r, &in); err != nil || in.Enabled == nil {
		fail(w, http.StatusBadRequest, "invalid settings")
		return
	}
	var enabled bool
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(r.Context(), tx, p, "settings.manage", authz.Scope{}); err != nil {
			return err
		}
		err := tx.QueryRow(r.Context(), `SELECT enabled FROM portal_settings`).Scan(&enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			enabled = false
			err = nil
		}
		if err != nil {
			return err
		}
		if enabled == *in.Enabled {
			return nil
		}
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO portal_settings(tenant_id, enabled)
			VALUES ($1::uuid, $2)
			ON CONFLICT (tenant_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = clock_timestamp()`,
			p.TenantID, *in.Enabled); err != nil {
			return err
		}
		enabled = *in.Enabled
		_, err = events.Append(r.Context(), tx, p, events.Change{
			Type:  "portal.settings_updated",
			After: map[string]any{"enabled": enabled},
		})
		return err
	})
	if errors.Is(err, authz.ErrForbidden) {
		fail(w, http.StatusForbidden, "permission denied")
		return
	}
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	write(w, http.StatusOK, portalSettings{Enabled: enabled})
}
