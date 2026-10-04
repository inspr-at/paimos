// SPDX-License-Identifier: AGPL-3.0-only

// Package tenantbrand is a workspace's own brand in the header: a short name
// and a logo, with an optional second logo for dark mode (AEON-431). The
// product's names stay in package brand; this one never replaces them in the
// footer, the sign-in page or the About menu.
//
// Writes are person-only and need settings.manage. Each change appends a
// tenant.brand_updated event. Logos are served only to the workspace's own
// members, as images with nosniff and a CSP that allows nothing, and the web
// app renders them only through <img>.
//
// No business logo was reused: the quote document profile carries a logo
// reference but has no upload for it, so there is no tenant company logo to
// offer as the default (checked 2026-09-30).
package tenantbrand

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// EventType is appended for every change to the brand.
const EventType = "tenant.brand_updated"

const maxShortName = 32

// Module serves the brand settings and the logo images.
type Module struct {
	pool *pgxpool.Pool
	// decoding admits one logo check at a time. What a single decode may
	// allocate is bounded by the pixel limit and by the decoder (see
	// maxLogoPixels); this bound is our own and holds whatever the decoder
	// does: however many uploads arrive at once, only one decode's memory is
	// live.
	decoding chan struct{}
}

func New(pool *pgxpool.Pool) *Module {
	return &Module{pool: pool, decoding: make(chan struct{}, 1)}
}

func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/brand", m.settings(false, m.get))
	mux.HandleFunc("PUT /api/settings/brand", m.settings(true, m.putName))
	mux.HandleFunc("PUT /api/settings/brand/logo/{variant}", m.settings(true, m.putLogo))
	mux.HandleFunc("DELETE /api/settings/brand/logo/{variant}", m.settings(true, m.deleteLogo))
	mux.HandleFunc("GET /api/brand/logo/{variant}", m.serveLogo)
}

// LogoRef is a logo as the header needs it.
type LogoRef struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Public is the brand in the session payload (GET /api/me, tenant.brand).
// Nil when the workspace has set nothing.
type Public struct {
	ShortName string   `json:"short_name,omitempty"`
	Logo      *LogoRef `json:"logo,omitempty"`
	LogoDark  *LogoRef `json:"logo_dark,omitempty"`
}

// LogoInfo is a stored logo as the settings form shows it.
type LogoInfo struct {
	LogoRef
	ContentType string    `json:"content_type"`
	Size        int       `json:"size"`
	SHA256      string    `json:"sha256"`
	UploadedAt  time.Time `json:"uploaded_at"`
}

// Settings is GET /api/settings/brand and the answer to every write.
type Settings struct {
	ShortName string     `json:"short_name"`
	Logo      *LogoInfo  `json:"logo"`
	LogoDark  *LogoInfo  `json:"logo_dark"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	// Cleaned is set when harmless SVG comments, non-drawing attributes or metadata were removed.
	Cleaned    bool        `json:"cleaned,omitempty"`
	SVGCleanup *SVGCleanup `json:"svg_cleanup,omitempty"`
}

type failure struct {
	status int
	code   string
	msg    string
}

func (f failure) Error() string { return f.msg }

func fail(status int, msg string) error { return failure{status: status, msg: msg} }

// codeLogoTooLarge is the stable code of every 413 on a logo upload.
const codeLogoTooLarge = "logo_too_large"

func writeFailure(w http.ResponseWriter, f failure) {
	if f.code == "" {
		httpapi.WriteError(w, f.status, f.msg)
		return
	}
	httpapi.WriteJSON(w, f.status, struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}{f.msg, f.code})
}

// settings wraps a person-only settings.manage route in a tenant transaction.
func (m *Module) settings(write bool, fn func(*http.Request, pgx.Tx, tenant.Principal) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		p, ok := tenant.PrincipalFrom(r.Context())
		if !ok || p.ID == "" || p.TenantID == "" {
			httpapi.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if p.Kind != tenant.Person {
			httpapi.WriteError(w, http.StatusForbidden, "person required")
			return
		}
		if write {
			if err := httpapi.BufferRequestBody(w, r, MaxLogoBytes); err != nil {
				var tooBig *http.MaxBytesError
				if errors.As(err, &tooBig) {
					writeFailure(w, failure{http.StatusRequestEntityTooLarge, codeLogoTooLarge, "the logo exceeds 256 KB"})
				} else {
					httpapi.WriteError(w, http.StatusBadRequest, "request body could not be read within limits")
				}
				return
			}
		}
		var out any
		err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
			if write {
				// A brand may not have a row yet; all name and logo variants
				// share this fence so event preimages reflect the last commit.
				if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, p.TenantID+":tenant-brand"); err != nil {
					return err
				}
			}
			if err := authz.RequireTx(r.Context(), tx, p, "settings.manage", authz.Scope{}); err != nil {
				return err
			}
			var err error
			out, err = fn(r, tx, p)
			return err
		})
		var f failure
		var tooBig *http.MaxBytesError
		switch {
		case err == nil:
			httpapi.WriteJSON(w, http.StatusOK, out)
		case errors.As(err, &f):
			writeFailure(w, f)
		case errors.As(err, &tooBig):
			writeFailure(w, failure{http.StatusRequestEntityTooLarge, codeLogoTooLarge, "the logo exceeds 256 KB"})
		case errors.Is(err, authz.ErrForbidden):
			authz.WriteForbidden(w, err)
		default:
			httpapi.WriteError(w, http.StatusInternalServerError, "internal error")
		}
	}
}

func variant(r *http.Request) (string, error) {
	switch v := r.PathValue("variant"); v {
	case "light", "dark":
		return v, nil
	}
	return "", fail(http.StatusNotFound, "unknown logo variant")
}

func (m *Module) get(r *http.Request, tx pgx.Tx, _ tenant.Principal) (any, error) {
	return load(r.Context(), tx)
}

func (m *Module) putName(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		ShortName *string `json:"short_name"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || in.ShortName == nil {
		return nil, fail(http.StatusBadRequest, "expected {\"short_name\": \"…\"}")
	}
	name, err := cleanShortName(*in.ShortName)
	if err != nil {
		return nil, err
	}
	before, err := load(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	if before.ShortName == name {
		return before, nil
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO tenant_brand (tenant_id, short_name, updated_by_principal_id) VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id) DO UPDATE SET short_name = EXCLUDED.short_name, updated_by_principal_id = EXCLUDED.updated_by_principal_id, updated_at = now()`,
		p.TenantID, name, p.ID); err != nil {
		return nil, err
	}
	return m.record(r.Context(), tx, p, before)
}

// checkLogo runs ValidateLogo once no other check is running.
func (m *Module) checkLogo(ctx context.Context, declared string, body []byte) (Logo, error) {
	select {
	case m.decoding <- struct{}{}:
	case <-ctx.Done():
		return Logo{}, ctx.Err()
	}
	defer func() { <-m.decoding }()
	return ValidateLogo(declared, body)
}

func (m *Module) putLogo(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	v, err := variant(r)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	logo, err := m.checkLogo(r.Context(), r.Header.Get("Content-Type"), body)
	var tooLarge TooLargeError
	switch {
	case errors.As(err, &tooLarge):
		return nil, failure{http.StatusRequestEntityTooLarge, codeLogoTooLarge, err.Error()}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil, err
	case err != nil:
		return nil, fail(http.StatusBadRequest, err.Error())
	}
	before, err := load(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(logo.Content)
	hash := hex.EncodeToString(sum[:])
	if current := pick(before, v); current != nil && current.SHA256 == hash {
		before.Cleaned, before.SVGCleanup = logo.Cleaned, logo.SVGCleanup
		return before, nil
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO tenant_brand_logos (tenant_id, variant, content_type, content, sha256, width, height, uploaded_by_principal_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (tenant_id, variant) DO UPDATE SET content_type = EXCLUDED.content_type, content = EXCLUDED.content, sha256 = EXCLUDED.sha256,
		  width = EXCLUDED.width, height = EXCLUDED.height, uploaded_by_principal_id = EXCLUDED.uploaded_by_principal_id, uploaded_at = now()`,
		p.TenantID, v, logo.ContentType, logo.Content, hash, logo.Width, logo.Height, p.ID); err != nil {
		return nil, err
	}
	out, err := m.record(r.Context(), tx, p, before)
	if err != nil {
		return nil, err
	}
	out.Cleaned, out.SVGCleanup = logo.Cleaned, logo.SVGCleanup
	return out, nil
}

func (m *Module) deleteLogo(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	v, err := variant(r)
	if err != nil {
		return nil, err
	}
	before, err := load(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	if pick(before, v) == nil {
		return before, nil
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM tenant_brand_logos WHERE variant = $1`, v); err != nil {
		return nil, err
	}
	return m.record(r.Context(), tx, p, before)
}

// record appends the audit event for a change and returns the new state.
func (m *Module) record(ctx context.Context, tx pgx.Tx, p tenant.Principal, before Settings) (Settings, error) {
	after, err := load(ctx, tx)
	if err != nil {
		return Settings{}, err
	}
	if _, err := events.Append(ctx, tx, p, events.Change{Type: EventType, Before: snapshot(before), After: snapshot(after)}); err != nil {
		return Settings{}, err
	}
	return after, nil
}

// snapshot is the audit view: the name and each logo's identity, not its bytes.
func snapshot(s Settings) map[string]any {
	logo := func(l *LogoInfo) any {
		if l == nil {
			return nil
		}
		return map[string]any{"sha256": l.SHA256, "content_type": l.ContentType, "width": l.Width, "height": l.Height, "size": l.Size}
	}
	return map[string]any{"short_name": s.ShortName, "logo": logo(s.Logo), "logo_dark": logo(s.LogoDark)}
}

func pick(s Settings, v string) *LogoInfo {
	if v == "dark" {
		return s.LogoDark
	}
	return s.Logo
}

func cleanShortName(raw string) (string, error) {
	name := strings.Join(strings.Fields(raw), " ")
	if utf8.RuneCountInString(name) > maxShortName {
		return "", fail(http.StatusBadRequest, "the short name may have at most 32 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '<' || r == '>' || unicode.Is(unicode.Cf, r) {
			return "", fail(http.StatusBadRequest, "the short name has a character that cannot be shown")
		}
	}
	return name, nil
}

func logoURL(v, sha string) string { return "/api/brand/logo/" + v + "?v=" + sha[:16] }

// load reads the brand of the transaction's tenant.
func load(ctx context.Context, tx pgx.Tx) (Settings, error) {
	var s Settings
	var updated time.Time
	err := tx.QueryRow(ctx, `SELECT short_name, updated_at FROM tenant_brand`).Scan(&s.ShortName, &updated)
	switch {
	case err == nil:
		s.UpdatedAt = &updated
	case !errors.Is(err, pgx.ErrNoRows):
		return Settings{}, err
	}
	rows, err := tx.Query(ctx, `SELECT variant, content_type, octet_length(content), sha256, width, height, uploaded_at FROM tenant_brand_logos`)
	if err != nil {
		return Settings{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		var l LogoInfo
		if err := rows.Scan(&v, &l.ContentType, &l.Size, &l.SHA256, &l.Width, &l.Height, &l.UploadedAt); err != nil {
			return Settings{}, err
		}
		l.URL = logoURL(v, l.SHA256)
		if v == "dark" {
			s.LogoDark = &l
		} else {
			s.Logo = &l
		}
		if s.UpdatedAt == nil || l.UploadedAt.After(*s.UpdatedAt) {
			at := l.UploadedAt
			s.UpdatedAt = &at
		}
	}
	return s, rows.Err()
}

// Load returns the brand for the session payload, or nil when nothing is set.
// It runs in the caller's db.InTenant transaction.
func Load(ctx context.Context, tx pgx.Tx) (*Public, error) {
	s, err := load(ctx, tx)
	if err != nil {
		return nil, err
	}
	ref := func(l *LogoInfo) *LogoRef {
		if l == nil {
			return nil
		}
		r := l.LogoRef
		return &r
	}
	out := &Public{ShortName: s.ShortName, Logo: ref(s.Logo), LogoDark: ref(s.LogoDark)}
	if out.ShortName == "" && out.Logo == nil && out.LogoDark == nil {
		return nil, nil
	}
	return out, nil
}

// serveLogo answers GET /api/brand/logo/{variant} for any member of the
// workspace. The response can never become a document: nosniff, a CSP that
// allows nothing and a sandbox, and only the three image types.
func (m *Module) serveLogo(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		httpapi.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	v, err := variant(r)
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, "logo not found")
		return
	}
	var contentType, sha string
	var content []byte
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT content_type, content, sha256 FROM tenant_brand_logos WHERE variant = $1`, v).Scan(&contentType, &content, &sha)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		w.Header().Set("Cache-Control", "no-store")
		httpapi.WriteError(w, http.StatusNotFound, "logo not found")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Content-Disposition", "inline")
	h.Set("ETag", `"`+sha+`"`)
	// The session payload links a logo with its hash, so that URL never changes.
	if r.URL.Query().Get("v") == sha[:16] {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "private, no-cache")
	}
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, `"`+sha+`"`) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(content)
}
