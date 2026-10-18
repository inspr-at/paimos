// SPDX-License-Identifier: AGPL-3.0-only

package activity

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type module struct{ pool *pgxpool.Pool }

// New returns the core node activity module for the coordinator to mount.
func New(pool *pgxpool.Pool) httpapi.Module { return &module{pool: pool} }

func (m *module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/nodes/{nodeId}/activity", m.list)
	mux.HandleFunc("POST /api/nodes/{nodeId}/comments", m.comment)
	mux.HandleFunc("PATCH /api/nodes/{nodeId}/comments/{commentId}", m.comment)
	mux.HandleFunc("DELETE /api/nodes/{nodeId}/comments/{commentId}", m.comment)
}

var (
	errInvalid   = errors.New("invalid request")
	errForbidden = errors.New("only the author can edit or delete a comment within 15 minutes of creation")
)

func failure(w http.ResponseWriter, err error) {
	status, code, message := 500, "internal_error", "internal error"
	switch {
	case errors.Is(err, errInvalid):
		status, code, message = 400, "invalid_request", "invalid node, comment, cursor, limit or body_markdown"
	case errors.Is(err, errForbidden):
		status, code, message = 403, "forbidden", errForbidden.Error()
	case errors.Is(err, pgx.ErrNoRows):
		status, code, message = 404, "not_found", "node or comment not found"
	}
	httpapi.WriteJSON(w, status, map[string]string{"code": code, "message": message})
}

func requestPrincipal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		httpapi.WriteJSON(w, 401, map[string]string{"code": "unauthorized", "message": "authentication required"})
		return p, false
	}
	var id pgtype.UUID
	if len(r.PathValue("nodeId")) != 36 || id.Scan(r.PathValue("nodeId")) != nil || !id.Valid {
		failure(w, errInvalid)
		return p, false
	}
	return p, true
}

func (m *module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := requestPrincipal(w, r)
	if !ok {
		return
	}
	limit := 50
	var err error
	if r.URL.Query().Has("limit") {
		limit, err = strconv.Atoi(r.URL.Query().Get("limit"))
	}
	if err != nil || limit < 1 || limit > 200 {
		failure(w, errInvalid)
		return
	}
	var c *cursor
	if r.URL.Query().Has("cursor") {
		c, err = decodeCursor(r.URL.Query().Get("cursor"), p.TenantID, r.PathValue("nodeId"))
		if err != nil {
			failure(w, errInvalid)
			return
		}
	}
	page, err := m.read(r.Context(), p, r.PathValue("nodeId"), limit, c)
	if err != nil {
		failure(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, page)
}

func (m *module) comment(w http.ResponseWriter, r *http.Request) {
	p, ok := requestPrincipal(w, r)
	if !ok {
		return
	}
	var id int64
	var err error
	if r.Method != http.MethodPost {
		id, err = strconv.ParseInt(r.PathValue("commentId"), 10, 64)
		if err != nil || id < 1 || strconv.FormatInt(id, 10) != r.PathValue("commentId") {
			failure(w, errInvalid)
			return
		}
	}
	var body struct {
		Markdown string `json:"body_markdown"`
	}
	if r.Method != http.MethodDelete {
		dec := httpapi.NewRequestDecoder(w, r, 512<<10)
		dec.DisallowUnknownFields()
		if dec.Decode(&body) != nil || dec.Decode(new(any)) != io.EOF || !utf8.ValidString(body.Markdown) || strings.TrimSpace(body.Markdown) == "" || len(body.Markdown) > 65536 {
			failure(w, errInvalid)
			return
		}
	}
	item, err := m.writeComment(r.Context(), p, r.PathValue("nodeId"), id, body.Markdown, r.Method == http.MethodDelete)
	if err != nil {
		failure(w, err)
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(204)
		return
	}
	status := 200
	if r.Method == http.MethodPost {
		status = 201
	}
	httpapi.WriteJSON(w, status, item)
}
