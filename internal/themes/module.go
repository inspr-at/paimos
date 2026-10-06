// SPDX-License-Identifier: AGPL-3.0-only
package themes

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type Module struct{ Store Store }

var _ httpapi.Module = (*Module)(nil)

func New(pool *pgxpool.Pool) *Module { return &Module{Store{Pool: pool}} }
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/themes", m.list)
	mux.HandleFunc("POST /api/themes", m.create)
	mux.HandleFunc("GET /api/themes/{themeId}", m.get)
	mux.HandleFunc("PATCH /api/themes/{themeId}", m.update)
	mux.HandleFunc("DELETE /api/themes/{themeId}", m.delete)
	mux.HandleFunc("POST /api/themes/{themeId}/duplicate", m.duplicate)
	mux.HandleFunc("GET /api/me/theme", m.active)
	mux.HandleFunc("PUT /api/me/theme", m.selectTheme)
}
func actor(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !validUUID(p.ID) || !validUUID(p.TenantID) {
		httpapi.WriteError(w, 401, "authentication required")
		return p, false
	}
	w.Header().Set("Cache-Control", "no-store")
	return p, true
}
func failure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalid):
		httpapi.WriteError(w, 400, ErrInvalid.Error())
	case errors.Is(err, authz.ErrForbidden):
		httpapi.WriteError(w, 403, "theme operation is not permitted")
	case errors.Is(err, pgx.ErrNoRows):
		httpapi.WriteError(w, 404, "theme not found")
	case errors.Is(err, ErrConflict):
		httpapi.WriteError(w, 409, ErrConflict.Error())
	default:
		httpapi.WriteError(w, 500, "theme operation failed")
	}
}

// Required shape checks retain the distinction between omission and explicit
// null (derived dark/native artwork/default selection) without allowing zero
// values to silently stand in for an incomplete replacement config.
func object(raw json.RawMessage, required ...string) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, false
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return nil, false
		}
	}
	return fields, true
}
func completeValues(raw json.RawMessage) bool {
	v, ok := object(raw, "primary", "secondary", "recurring_marker", "agents")
	if !ok {
		return false
	}
	for _, key := range []string{"primary", "secondary"} {
		if _, ok := object(v[key], "light", "dark"); !ok {
			return false
		}
	}
	if _, ok := object(v["recurring_marker"], "source", "custom"); !ok {
		return false
	}
	a, ok := object(v["agents"], "avatar", "ring", "hover", "size", "palette")
	if !ok || string(a["hover"]) == "null" {
		return false
	}
	for _, key := range []string{"dim_inactive", "inactive_opacity"} {
		if raw, present := a[key]; present && string(raw) == "null" {
			return false
		}
	}
	return true
}
func input(w http.ResponseWriter, r *http.Request, out any, required ...string) bool {
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(10 * time.Second)); err == nil {
		defer controller.SetReadDeadline(time.Time{})
	} else if !errors.Is(err, http.ErrNotSupported) {
		httpapi.WriteError(w, 500, "theme request deadline could not be set")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var size *http.MaxBytesError
		if errors.As(err, &size) {
			httpapi.WriteError(w, 413, "theme request exceeds 8 KiB")
		} else if errors.Is(err, os.ErrDeadlineExceeded) {
			httpapi.WriteError(w, 408, "theme request body timed out")
		} else {
			httpapi.WriteError(w, 400, "invalid theme JSON")
		}
		return false
	}
	fields, ok := object(raw, required...)
	if !ok || !utf8.Valid(raw) {
		failure(w, ErrInvalid)
		return false
	}
	for key, value := range fields {
		if string(value) == "null" && key != "theme_id" {
			failure(w, ErrInvalid)
			return false
		}
	}
	if v, present := fields["values"]; present && !completeValues(v) {
		failure(w, ErrInvalid)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		failure(w, ErrInvalid)
		return false
	}
	return true
}
func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := actor(w, r)
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			failure(w, ErrInvalid)
			return
		}
	}
	out, err := m.Store.List(r.Context(), p, r.URL.Query().Get("after"), limit)
	if err != nil {
		failure(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) get(w http.ResponseWriter, r *http.Request) {
	p, ok := actor(w, r)
	if !ok {
		return
	}
	out, err := m.Store.Get(r.Context(), p, r.PathValue("themeId"))
	if err != nil {
		failure(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	p, ok := actor(w, r)
	if !ok {
		return
	}
	var in CreateInput
	if !input(w, r, &in, "name", "scope") {
		return
	}
	out, err := m.Store.Create(r.Context(), p, in)
	if err != nil {
		failure(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, out)
}
func (m *Module) update(w http.ResponseWriter, r *http.Request) {
	p, ok := actor(w, r)
	if !ok {
		return
	}
	var in UpdateInput
	if !input(w, r, &in, "revision") {
		return
	}
	out, err := m.Store.Update(r.Context(), p, r.PathValue("themeId"), in)
	if err != nil {
		failure(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) delete(w http.ResponseWriter, r *http.Request) {
	p, ok := actor(w, r)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	if err != nil {
		failure(w, ErrInvalid)
		return
	}
	if err := m.Store.Delete(r.Context(), p, r.PathValue("themeId"), revision); err != nil {
		failure(w, err)
		return
	}
	w.WriteHeader(204)
}
func (m *Module) duplicate(w http.ResponseWriter, r *http.Request) {
	p, ok := actor(w, r)
	if !ok {
		return
	}
	var in DuplicateInput
	if !input(w, r, &in, "name", "scope", "revision") {
		return
	}
	out, err := m.Store.Duplicate(r.Context(), p, r.PathValue("themeId"), in)
	if err != nil {
		failure(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, out)
}
func (m *Module) active(w http.ResponseWriter, r *http.Request) {
	p, ok := actor(w, r)
	if !ok {
		return
	}
	out, err := m.Store.Active(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) selectTheme(w http.ResponseWriter, r *http.Request) {
	p, ok := actor(w, r)
	if !ok {
		return
	}
	var in SelectionInput
	if !input(w, r, &in, "theme_id", "revision") {
		return
	}
	out, err := m.Store.Select(r.Context(), p, in)
	if err != nil {
		failure(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
