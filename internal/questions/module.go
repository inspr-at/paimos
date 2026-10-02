// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Module struct {
	pool *pgxpool.Pool
	// Production uses the database clock; deterministic tests inject one clock for decisions and dispatch.
	clock func(context.Context, pgx.Tx) (time.Time, error)
}

func (m *Module) now(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	if m.clock != nil {
		return m.clock(ctx, tx)
	}
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}

func New(pool *pgxpool.Pool) *Module { return &Module{pool: pool} }
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/projects/{projectId}/questions", m.handleAsk)
	mux.HandleFunc("GET /api/projects/{projectId}/questions", m.handleList)
	mux.HandleFunc("GET /api/questions/{questionId}", m.handleGet)
	mux.HandleFunc("GET /api/questions/{questionId}/status", m.handleGet)
	mux.HandleFunc("POST /api/questions/{questionId}/decision", m.handleDecide)
	mux.HandleFunc("GET /api/decision-desk", m.handleList)
}

type apiError struct {
	status        int
	code, message string
}

func (e *apiError) Error() string                 { return e.message }
func fail(status int, code, message string) error { return &apiError{status, code, message} }
func missing() error                              { return fail(404, "not_found", "question or referenced resource not found") }
func result(w http.ResponseWriter, status int, v any, err error) {
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = missing()
		}
		var pe *pgconn.PgError
		if errors.As(err, &pe) && (pe.Code == "40P01" || pe.Code == "40001") {
			err = fail(409, "retryable_conflict", "concurrent update; retry the same request")
		}
		var e *apiError
		if !errors.As(err, &e) {
			e = &apiError{500, "internal", "question operation failed"}
		}
		httpapi.WriteJSON(w, e.status, map[string]string{"code": e.code, "error": e.message})
		return
	}
	httpapi.WriteJSON(w, status, v)
}
func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !uuidRE.MatchString(p.ID) || !uuidRE.MatchString(p.TenantID) {
		result(w, 0, nil, fail(401, "unauthorized", "authentication required"))
		return p, false
	}
	return p, true
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			return fail(413, "too_large", "body exceeds 64 KiB")
		}
		return fail(400, "invalid_request", "invalid JSON or unknown field")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			return fail(413, "too_large", "body exceeds 64 KiB")
		}
		return fail(400, "invalid_request", "one JSON object required")
	}
	return nil
}
func (m *Module) handleAsk(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project := r.PathValue("projectId")
	if !uuidRE.MatchString(project) {
		result(w, 0, nil, missing())
		return
	}
	var in Input
	if err := decode(w, r, &in); err != nil {
		result(w, 0, nil, err)
		return
	}
	if err := in.Validate(); err != nil {
		result(w, 0, nil, fail(400, "invalid_request", err.Error()))
		return
	}
	q, replay, err := m.ask(r.Context(), p, project, in)
	status := 201
	if replay {
		status = 200
	}
	result(w, status, q, err)
}
func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("questionId")
	if !uuidRE.MatchString(id) {
		result(w, 0, nil, missing())
		return
	}
	q, err := m.get(r.Context(), p, id)
	result(w, 200, q, err)
}
func (m *Module) handleDecide(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	// Bearer credentials never acquire human authority, including injected
	// person contexts in internal callers that accidentally retain a key header.
	if p.Kind != tenant.Person || r.Header.Get("Authorization") != "" {
		result(w, 0, nil, fail(403, "person_required", "a signed-in person must decide"))
		return
	}
	id := r.PathValue("questionId")
	if !uuidRE.MatchString(id) {
		result(w, 0, nil, missing())
		return
	}
	var in DecisionInput
	if err := decode(w, r, &in); err != nil {
		result(w, 0, nil, err)
		return
	}
	if err := in.validate(); err != nil {
		result(w, 0, nil, fail(400, "invalid_request", err.Error()))
		return
	}
	q, err := m.decide(r.Context(), p, id, in)
	result(w, 200, q, err)
}
func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project := r.PathValue("projectId")
	if project != "" && !uuidRE.MatchString(project) {
		result(w, 0, nil, missing())
		return
	}
	limit, offset := 50, 0
	for _, v := range []struct {
		name     string
		dst      *int
		min, max int
	}{{"limit", &limit, 1, 100}, {"offset", &offset, 0, 100000}} {
		if r.URL.Query().Has(v.name) {
			n, e := strconv.Atoi(r.URL.Query().Get(v.name))
			if e != nil || n < v.min || n > v.max {
				result(w, 0, nil, fail(400, "invalid_request", "invalid pagination"))
				return
			}
			*v.dst = n
		}
	}
	state := r.URL.Query().Get("state")
	if state != "" && state != "open" && state != "answered" {
		result(w, 0, nil, fail(400, "invalid_request", "invalid state"))
		return
	}
	page, err := m.list(r.Context(), p, project, state, limit, offset)
	result(w, 200, page, err)
}
