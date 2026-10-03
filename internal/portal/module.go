// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

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
	prefixPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)
	errClosed     = errors.New("portal closed")
)

// Module serves the public catalog and the tenant switch that keeps it off.
type Module struct {
	pool          *pgxpool.Pool
	secureCookies bool
	macKey        []byte
}

var _ httpapi.Module = (*Module)(nil)
var _ httpapi.PublicModule = (*Module)(nil)

// New returns the portal module. secureCookies is true outside local dev, matching session cookies.
// macKey is the server session key used to HMAC public limiter buckets. It is copied and never logged.
func New(pool *pgxpool.Pool, secureCookies bool, macKey []byte) *Module {
	copied := make([]byte, len(macKey))
	copy(copied, macKey)
	return &Module{pool: pool, secureCookies: secureCookies, macKey: copied}
}

func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/portal/products/{productId}/pace", m.readPace)
	mux.HandleFunc("PUT /api/portal/products/{productId}/pace", m.writePace)
	mux.HandleFunc("GET /api/portal/products/{productId}/market", m.readMarket)
	mux.HandleFunc("GET /api/portal/products/{productId}/settings", m.readProductSettings)
	mux.HandleFunc("PUT /api/portal/products/{productId}/settings", m.writeProductSettings)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/participation", m.readParticipation)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/products/{productSlug}/participation", m.readParticipation)
	mux.HandleFunc("GET /api/portal/settings", m.readSettings)
	mux.HandleFunc("PATCH /api/portal/settings", m.updateSettings)
	mux.HandleFunc("POST /api/portal/wishes/{wishId}/publish", m.publishWish)
	mux.HandleFunc("POST /api/portal/wishes/{wishId}/reject", m.rejectWish)
	mux.HandleFunc("POST /api/portal/wishes/{wishId}/hide", m.hideWish)
	mux.HandleFunc("PATCH /api/portal/products/{productId}", m.editProduct)
	mux.HandleFunc("PATCH /api/portal/features/{featureId}", m.editFeature)
	mux.HandleFunc("GET /api/portal/market", m.readMarket)
	mux.HandleFunc("POST /api/portal/competitors", m.createCompetitor)
	mux.HandleFunc("PATCH /api/portal/competitors/{competitorId}", m.patchCompetitor)
	mux.HandleFunc("DELETE /api/portal/competitors/{competitorId}", m.deleteCompetitor)
	mux.HandleFunc("POST /api/portal/aspects", m.createAspect)
	mux.HandleFunc("PATCH /api/portal/aspects/{aspectId}", m.patchAspect)
	mux.HandleFunc("DELETE /api/portal/aspects/{aspectId}", m.deleteAspect)
	mux.HandleFunc("PUT /api/portal/cells", m.putCell)
	mux.HandleFunc("POST /api/portal/cells/{cellId}/approve", m.approveCell)
	mux.HandleFunc("POST /api/portal/corrections/{correctionId}/close", m.closeCorrection)
	mux.HandleFunc("GET /api/portal/pace", m.readPace)
	mux.HandleFunc("PUT /api/portal/pace", m.writePace)
	mux.HandleFunc("PUT /api/portal/wishes/{wishId}/fulfillment", m.writeFulfillment)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/products/{productSlug}", m.read)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/products/{productSlug}/catalog", m.read)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/products/{productSlug}/wishes", m.read)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/products/{productSlug}/comparison", m.read)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/products/{productSlug}/pace", m.read)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/products/{productSlug}/catalog.json", m.catalogFile)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/products/{productSlug}/llms.txt", m.llms)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/products/{productSlug}/releases", m.releases)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/products/{productSlug}/roadmap", m.roadmap)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/products/{productSlug}/roadmap.json", m.roadmap)
	mux.HandleFunc("POST /api/public/portal/{tenantSlug}/products/{productSlug}/wishes", m.submitWish)
	mux.HandleFunc("POST /api/public/portal/{tenantSlug}/products/{productSlug}/wishes/{wishKey}/votes", m.vote)
	mux.HandleFunc("POST /api/public/portal/{tenantSlug}/products/{productSlug}/corrections", m.submitCorrection)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}", m.read)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/catalog.json", m.catalogFile)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/llms.txt", m.llms)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/releases", m.releases)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/roadmap", m.roadmap)
	mux.HandleFunc("GET /api/public/portal/{tenantSlug}/roadmap.json", m.roadmap)
	mux.HandleFunc("POST /api/public/portal/{tenantSlug}/wishes", m.submitWish)
	mux.HandleFunc("POST /api/public/portal/{tenantSlug}/wishes/{wishKey}/votes", m.vote)
	mux.HandleFunc("POST /api/public/portal/{tenantSlug}/corrections", m.submitCorrection)
}

// MountPublic serves the same public files at the site root. The patterns are
// exact, so the Vue page at /portal/{tenantSlug} stays on the SPA.
func (m *Module) MountPublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /portal/{tenantSlug}/products/{productSlug}/llms.txt", m.llms)
	mux.HandleFunc("GET /portal/{tenantSlug}/products/{productSlug}/catalog.json", m.catalogFile)
	mux.HandleFunc("GET /portal/{tenantSlug}/products/{productSlug}/roadmap.json", m.roadmap)
	mux.HandleFunc("GET /portal/{tenantSlug}/llms.txt", m.llms)
	mux.HandleFunc("GET /portal/{tenantSlug}/catalog.json", m.catalogFile)
	mux.HandleFunc("GET /portal/{tenantSlug}/roadmap.json", m.roadmap)
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
	Enabled bool   `json:"enabled"`
	Slug    string `json:"slug"`
}

type settingsWrite struct {
	Enabled *bool `json:"enabled"`
}

func (m *Module) person(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !uuidPattern.MatchString(p.TenantID) || !uuidPattern.MatchString(p.ID) {
		fail(w, http.StatusUnauthorized, "sign in required")
		return tenant.Principal{}, false
	}
	// Portal settings are a person decision. An agent key is refused even when
	// its scopes name settings.manage, which is not an agent permission.
	if p.Kind != tenant.Person {
		fail(w, http.StatusForbidden, "permission denied")
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

const portalBodyTimeout = 10 * time.Second

// Decode before opening a transaction. A byte cap alone does not stop a slow
// sender; the socket deadline interrupts reads even when Body.Close would wait.
func decodePortalInput(w http.ResponseWriter, r *http.Request, decode func() error) bool {
	controller := http.NewResponseController(w)
	err := controller.SetReadDeadline(time.Now().Add(portalBodyTimeout))
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return false
	}
	if err == nil {
		defer controller.SetReadDeadline(time.Time{})
	}
	// Non-socket handlers (including isolated tests) still have a time bound.
	var timer *time.Timer
	if errors.Is(err, http.ErrNotSupported) && r.Body != nil {
		timer = time.AfterFunc(portalBodyTimeout, func() { _ = r.Body.Close() })
		defer timer.Stop()
	}
	if err := decode(); err != nil {
		var se statusError
		if errors.As(err, &se) {
			fail(w, se.status, se.msg)
		} else {
			fail(w, http.StatusBadRequest, "invalid input")
		}
		return false
	}
	return true
}

func (m *Module) decodeAdminInput(w http.ResponseWriter, r *http.Request, decode func() error) bool {
	publicHeaders(w)
	if _, ok := m.person(w, r); !ok {
		return false
	}
	if m.pool == nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return false
	}
	// Preserve permission-denial precedence without holding a transaction open
	// across body reads. manage/moderate still recheck under the write fence.
	err := authz.Require(authz.BindPool(r.Context(), m.pool), "settings.manage", authz.Scope{})
	if errors.Is(err, authz.ErrForbidden) {
		fail(w, http.StatusForbidden, "permission denied")
		return false
	}
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return false
	}
	return decodePortalInput(w, r, decode)
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
	var settings portalSettings
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(r.Context(), tx, p, "settings.manage", authz.Scope{}); err != nil {
			return err
		}
		var err error
		settings, err = loadSettings(r.Context(), tx)
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
	write(w, http.StatusOK, settings)
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
	if !decodePortalInput(w, r, func() error {
		if err := decodeJSON(r, &in); err != nil || in.Enabled == nil {
			return statusError{http.StatusBadRequest, "invalid settings"}
		}
		return nil
	}) {
		return
	}
	var settings portalSettings
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := lockPortalTenant(r.Context(), tx); err != nil {
			return err
		}
		if err := authz.RequireTx(r.Context(), tx, p, "settings.manage", authz.Scope{}); err != nil {
			return err
		}
		current, err := loadSettings(r.Context(), tx)
		if err != nil {
			return err
		}
		if current.Enabled == *in.Enabled {
			settings = current
			return nil
		}
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO portal_settings(tenant_id, enabled)
			VALUES ($1::uuid, $2)
			ON CONFLICT (tenant_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = clock_timestamp()`,
			p.TenantID, *in.Enabled); err != nil {
			return err
		}
		if _, err := events.Append(r.Context(), tx, p, events.Change{
			Type:  "portal.settings_updated",
			After: map[string]any{"enabled": *in.Enabled},
		}); err != nil {
			return err
		}
		current.Enabled = *in.Enabled
		settings = current
		return nil
	})
	if errors.Is(err, authz.ErrForbidden) {
		fail(w, http.StatusForbidden, "permission denied")
		return
	}
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	write(w, http.StatusOK, settings)
}

func loadSettings(ctx context.Context, tx pgx.Tx) (portalSettings, error) {
	var settings portalSettings
	err := tx.QueryRow(ctx, `SELECT enabled FROM portal_settings`).Scan(&settings.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		settings.Enabled = false
		err = nil
	}
	if err != nil {
		return settings, err
	}
	err = tx.QueryRow(ctx, `SELECT slug FROM tenants WHERE id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid`).Scan(&settings.Slug)
	if err != nil || !slugPattern.MatchString(settings.Slug) {
		if err == nil {
			err = errors.New("portal tenant slug")
		}
		return settings, err
	}
	return settings, nil
}
