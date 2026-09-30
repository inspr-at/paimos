// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/version"
)

// The generated manifest (data/history.json, written at build time and not
// committed) wins over the committed empty one.
//
//go:embed data/*.json
var data embed.FS

// Embedded returns the history built into this binary.
func Embedded() (History, error) {
	for _, name := range []string{"data/history.json", "data/empty.json"} {
		raw, err := data.ReadFile(name)
		if err != nil {
			continue
		}
		var h History
		if err := json.Unmarshal(raw, &h); err != nil {
			return History{}, fmt.Errorf("%s: %w", name, err)
		}
		if h.Schema != Schema {
			return History{}, fmt.Errorf("%s: schema %q, want %q", name, h.Schema, Schema)
		}
		if h.Releases == nil {
			h.Releases = []Release{}
		}
		return h, nil
	}
	return History{}, fmt.Errorf("no embedded release history")
}

// Module serves a History over HTTP.
type Module struct {
	pool       *pgxpool.Pool
	projectKey string
	history    History
	current    string
	started    time.Time
	tickets    TicketSource
}

// New serves the embedded history; current is the running version.
func New() (*Module, error) {
	h, err := Embedded()
	if err != nil {
		return nil, err
	}
	return NewWith(h, version.Version), nil
}

// NewWith serves the given history, for tests and other products. The server
// creates its module at startup, so that moment is when the running version went
// live on this server.
func NewWith(h History, current string) *Module {
	return &Module{history: h, current: current, started: time.Now().UTC().Truncate(time.Second)}
}

// UseTickets classifies changes when the history is served. Call it before
// the server accepts requests. Without it, responses keep the embedded
// changes and clients derive groups from type.
func (m *Module) UseTickets(src TicketSource) {
	if m != nil {
		m.tickets = src
	}
}

var _ httpapi.Module = (*Module)(nil)

// Mount registers GET /api/releases, GET /api/releases/{version} and the
// presentation writes PUT and DELETE /api/releases/{version}/presentation.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/releases", m.list)
	mux.HandleFunc("GET /api/releases/{version}", m.one)
	mux.HandleFunc("PUT /api/releases/{version}/presentation", m.putPresentation)
	mux.HandleFunc("DELETE /api/releases/{version}/presentation", m.deletePresentation)
}

// Response is the history as served, with the running version and since when
// this server has run it.
type Response struct {
	History
	Current   string    `json:"current"`
	LiveSince time.Time `json:"live_since"`
}

func authorized(w http.ResponseWriter, r *http.Request) bool {
	if p, ok := tenant.PrincipalFrom(r.Context()); !ok || p.ID == "" {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return false
	}
	return true
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	if !authorized(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h, err := m.historyFor(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "release notes unavailable")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, Response{History: m.annotated(r.Context(), h), Current: m.current, LiveSince: m.started})
}

func (m *Module) one(w http.ResponseWriter, r *http.Request) {
	if !authorized(w, r) {
		return
	}
	v := strings.TrimPrefix(r.PathValue("version"), "v")
	if !ValidVersion(v) {
		httpapi.WriteError(w, http.StatusBadRequest, "not a calendar version")
		return
	}
	h, err := m.historyFor(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "release notes unavailable")
		return
	}
	for _, rel := range m.annotated(r.Context(), h).Releases {
		if rel.Version == v {
			w.Header().Set("Cache-Control", "no-store")
			httpapi.WriteJSON(w, http.StatusOK, rel)
			return
		}
	}
	httpapi.WriteError(w, http.StatusNotFound, "no such release in this build's history")
}

// annotated derives Aeon's groups from the selected frozen capture. A capture
// without a group, and a release with no capture, take only the group from the
// live classification of every commit ticket. Other products retain their
// legacy ticket source; lookup errors leave their embedded history unchanged.
func (m *Module) annotated(ctx context.Context, h History) History {
	if aeonHistory(h) {
		return m.annotateAeon(ctx, h)
	}
	if m.tickets == nil {
		return h
	}
	p, ok := tenant.PrincipalFrom(ctx)
	if !ok || p.TenantID == "" {
		return h
	}
	keys := historyTicketKeys(h)
	meta, err := m.tickets(ctx, p.TenantID, keys)
	if err != nil {
		slog.Warn("release change groups left derived from commit type", "err", err)
		return h
	}
	return withGroups(h, meta)
}

// annotateAeon keeps captured pill and benefit text. When the capture stored
// no group, or the release has no capture, the caller's live classification
// supplies features or fixes for every commit ticket. Live note text is dropped.
func (m *Module) annotateAeon(ctx context.Context, h History) History {
	keys := classificationLookupKeys(h)
	var live map[string]TicketMeta
	if len(keys) > 0 && m != nil && m.tickets != nil {
		if p, ok := tenant.PrincipalFrom(ctx); ok && p.TenantID != "" {
			meta, err := m.tickets(ctx, p.TenantID, keys)
			if err != nil {
				slog.Warn("release change groups left without live classification", "err", err)
			} else {
				live = meta
			}
		}
	}
	return withFrozenGroups(h, live)
}
