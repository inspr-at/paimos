// SPDX-License-Identifier: AGPL-3.0-only

package assistants

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Module struct{ pool *pgxpool.Pool }

var _ httpapi.Module = (*Module)(nil)
var _ PolicyReader = (*Module)(nil)

func New(pool *pgxpool.Pool) *Module { return &Module{pool: pool} }

func (m *Module) Mount(mux *http.ServeMux) {
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /api/assistants":                        m.list,
		"POST /api/assistants":                       m.write,
		"GET /api/assistants/{principalId}":          m.get,
		"PATCH /api/assistants/{principalId}":        m.write,
		"POST /api/assistants/{principalId}/disable": m.write,
		"GET /api/assistants/{principalId}/policy":   m.get,
		"PUT /api/assistants/{principalId}/policy":   m.write,
	} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if _, ok := tenant.PrincipalFrom(r.Context()); !ok {
				httpapi.WriteError(w, 401, "authentication required")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			handler(w, r.WithContext(ctx))
		})
	}
}

type createInput struct {
	PrincipalID   string `json:"principal_id"`
	DisplayName   string `json:"display_name"`
	Vendor        string `json:"vendor"`
	OwnerPersonID string `json:"owner_person_id"`
	Policy        Policy `json:"policy"`
}
type updateInput struct {
	ExpectedRevision *int64  `json:"expected_revision"`
	DisplayName      *string `json:"display_name,omitempty"`
	Vendor           *string `json:"vendor,omitempty"`
	Policy           *Policy `json:"policy,omitempty"`
}

func decode(w http.ResponseWriter, r *http.Request, out any) error {
	raw, err := httpapi.ReadRequestBody(w, r, 32768, 10*time.Second)
	if err != nil {
		return errInput
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errInput
	}
	if d.Decode(new(any)) != io.EOF {
		return errInput
	}
	return nil
}

func writeError(w http.ResponseWriter, err error) {
	var pe *pgconn.PgError
	switch {
	case errors.Is(err, authz.ErrForbidden), errors.Is(err, db.ErrKeyAuthorityChanged):
		httpapi.WriteError(w, 403, "permission denied")
	case errors.Is(err, ErrNotFound):
		httpapi.WriteError(w, 404, ErrNotFound.Error())
	case errors.Is(err, ErrConflict), errors.As(err, &pe) && pe.Code == "23505":
		httpapi.WriteError(w, 409, ErrConflict.Error())
	case errors.Is(err, errInput):
		httpapi.WriteError(w, 400, errInput.Error())
	default:
		slog.Error("assistants", "error", err)
		httpapi.WriteError(w, 500, "assistant operation failed")
	}
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.PrincipalFrom(r.Context())
	id := r.PathValue("principalId")
	if !uuid.MatchString(id) {
		writeError(w, errInput)
		return
	}
	policyOnly := r.Pattern == "GET /api/assistants/{principalId}/policy"
	var out Assistant
	err := db.InTenantReadSnapshot(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := readAuthority(r.Context(), tx, p, policyOnly, id); err != nil {
			return err
		}
		var err error
		out, err = getTx(r.Context(), tx, p.TenantID, id)
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if policyOnly {
		httpapi.WriteJSON(w, 200, out.Snapshot)
	} else {
		httpapi.WriteJSON(w, 200, out)
	}
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.PrincipalFrom(r.Context())
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, errInput)
			return
		}
	}
	after := r.URL.Query().Get("after")
	if after != "" && !uuid.MatchString(after) {
		writeError(w, errInput)
		return
	}
	out := struct {
		Assistants []Assistant `json:"assistants"`
		NextCursor string      `json:"next_cursor,omitempty"`
	}{Assistants: []Assistant{}}
	err := db.InTenantReadSnapshot(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := readAuthority(r.Context(), tx, p, false, ""); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), assistantSelect+` AND ($2='' OR p.id>NULLIF($2,'')::uuid) ORDER BY p.id LIMIT $3`, p.TenantID, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanAssistant(rows)
			if err != nil {
				return err
			}
			out.Assistants = append(out.Assistants, item)
		}
		return rows.Err()
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if len(out.Assistants) > limit {
		out.Assistants = out.Assistants[:limit]
		out.NextCursor = out.Assistants[limit-1].PrincipalID
	}
	httpapi.WriteJSON(w, 200, out)
}

func (m *Module) write(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.PrincipalFrom(r.Context())
	if p.Kind != tenant.Person {
		writeError(w, authz.ErrForbidden)
		return
	}
	id := r.PathValue("principalId")
	if id != "" && !uuid.MatchString(id) {
		writeError(w, errInput)
		return
	}
	if id == "" {
		var in createInput
		if err := decode(w, r, &in); err != nil {
			writeError(w, err)
			return
		}
		out, err := m.create(r.Context(), p, in)
		if err != nil {
			writeError(w, err)
			return
		}
		httpapi.WriteJSON(w, 201, out)
		return
	}
	disable := r.Pattern == "POST /api/assistants/{principalId}/disable"
	policyOnly := r.Method == http.MethodPut
	var in updateInput
	if !disable {
		if err := decode(w, r, &in); err != nil {
			writeError(w, err)
			return
		}
		if policyOnly && (in.DisplayName != nil || in.Vendor != nil) {
			writeError(w, errInput)
			return
		}
	}
	out, err := m.update(r.Context(), p, id, in, policyOnly, disable)
	if err != nil {
		writeError(w, err)
		return
	}
	if policyOnly {
		httpapi.WriteJSON(w, 200, out.Snapshot)
	} else {
		httpapi.WriteJSON(w, 200, out)
	}
}
