// SPDX-License-Identifier: AGPL-3.0-only

// Package host wires Aithema into Aeon's host authority, settings and HTTP
// boundaries. It never grants person decisions to delegated credentials.
package host

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/aithema/journal"
	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Module struct {
	Pool     *pgxpool.Pool
	Journal  *journal.Store
	Keys     *tokens.KeySet
	Issuer   string
	vaultKey []byte
	clock    func() time.Time
}
type Fault struct {
	Status int
	Code   string
}

func (f *Fault) Error() string            { return f.Code }
func fail(status int, code string) *Fault { return &Fault{status, code} }
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, v)
}
func writeError(w http.ResponseWriter, err error) {
	status, code := 503, "unavailable"
	var f *Fault
	var j *journal.Fault
	if errors.As(err, &f) {
		status, code = f.Status, f.Code
	} else if errors.As(err, &j) {
		status, code = j.Status, j.Code
	} else if errors.Is(err, authz.ErrForbidden) {
		status, code = 403, "forbidden"
	}
	reply(w, status, map[string]string{"error": code, "code": code})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return fail(413, "too_large")
	}
	// CanonicalJSON rejects duplicate keys and malformed Unicode before the
	// typed decoder; integer fields are then decoded without float conversion.
	if _, err := tokens.CanonicalJSON(raw); err != nil {
		return fail(400, "invalid_request")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return fail(400, "invalid_request")
	}
	if _, ok := v.(*settingsWrite); ok {
		for _, field := range []string{"service_url", "location", "plugin_principal_id", "preview_keys", "picker_sha256", "currency", "session_cap_micro", "principal_day_cap_micro", "tenant_day_cap_micro"} {
			if value, exists := fields[field]; !exists || bytes.Equal(value, []byte("null")) {
				return fail(400, "invalid_settings")
			}
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		return fail(400, "invalid_request")
	}
	return nil
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("UUID unavailable")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func New(pool *pgxpool.Pool, store *journal.Store, keys *tokens.KeySet, master []byte, issuer string) (*Module, error) {
	if len(master) < 32 || store == nil || keys != nil && !strings.HasPrefix(issuer, "https://") {
		return nil, fail(503, "unavailable")
	}
	sum := sha256.Sum256(append([]byte("aeon/aithema-host-settings-v1\x00"), master...))
	return &Module{Pool: pool, Journal: store, Keys: keys, Issuer: strings.TrimRight(issuer, "/"), vaultKey: sum[:], clock: time.Now}, nil
}

func Plugin() (plugins.Plugin, error) {
	p := plugins.Plugin{Manifest: plugins.Manifest{ID: "aithema", Version: "1", Owner: "aithema", Permissions: []string{"intake.read", "intake.write"}}}
	digest, err := plugins.Digest(p)
	p.Manifest.DigestSHA256 = digest
	return p, err
}
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/plugins/aithema/settings", m.getSettings)
	mux.HandleFunc("PUT /api/plugins/aithema/settings", m.putSettings)
	mux.HandleFunc("POST /api/projects/{projectId}/aithema/sessions", m.create)
	mux.HandleFunc("POST /api/projects/{projectId}/aithema/sessions/{sid}/tokens", m.refresh)
	mux.HandleFunc("POST /api/projects/{projectId}/aithema/sessions/{sid}/control", m.control)
	mux.HandleFunc("POST /api/projects/{projectId}/aithema/sessions/{sid}/host-event", m.hostEvent)
	mux.HandleFunc("POST /api/aithema/deprovision", m.deprovision)
	mux.HandleFunc("GET /api/aithema/callbacks/{callbackId}", m.callbackStatus)
	mux.HandleFunc("POST /api/projects/{projectId}/aithema/sessions/{sid}/proxy/{operation}", m.proxy)
	mux.HandleFunc("GET /api/projects/{projectId}/aithema/sessions/{sid}/proxy/{operation}", m.proxy)
}
func (m *Module) MountPublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /aithema/preview/{design_rev}", m.preview)
}

func (m *Module) person(r *http.Request, permission string, project string) (tenant.Principal, error) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.Kind != tenant.Person || !uuidRE.MatchString(p.ID) || !uuidRE.MatchString(p.TenantID) {
		return p, fail(403, "forbidden")
	}
	if err := authz.Require(authz.BindPool(r.Context(), m.Pool), permission, authz.Scope{ProjectID: project}); err != nil {
		return p, err
	}
	if m.Keys == nil {
		return p, fail(503, "unavailable")
	}
	return p, nil
}
func (m *Module) getSettings(w http.ResponseWriter, r *http.Request) {
	p, err := m.person(r, "plugins.manage", "")
	if err != nil {
		writeError(w, err)
		return
	}
	var out settingsRead
	err = db.InTenant(r.Context(), m.Pool, p.TenantID, func(tx pgx.Tx) error {
		s, c, err := loadSettings(r.Context(), tx, p.TenantID, false)
		out = masked(s, len(c) > 0)
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	reply(w, 200, out)
}
func (m *Module) putSettings(w http.ResponseWriter, r *http.Request) {
	p, err := m.person(r, "plugins.manage", "")
	if err != nil {
		writeError(w, err)
		return
	}
	if !m.sameOrigin(r) {
		writeError(w, fail(403, "forbidden"))
		return
	}
	var in settingsWrite
	if err := decode(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	out, err := m.saveSettings(r.Context(), p, in)
	if err != nil {
		writeError(w, err)
		return
	}
	reply(w, 200, out)
}
func (m *Module) sameOrigin(r *http.Request) bool {
	return len(r.Header.Values("Origin")) == 1 && r.Header.Get("Origin") == m.Issuer
}
