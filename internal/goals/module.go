// SPDX-License-Identifier: AGPL-3.0-only

package goals

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/reportercontract"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type module struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func New(pool *pgxpool.Pool) *module { return &module{pool: pool, now: time.Now} }
func (m *module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/goals/{goalId}/series", reportercontract.WithHeader(reportercontract.Goals, m.series))
	mux.HandleFunc("GET /api/goals", reportercontract.WithHeader(reportercontract.Goals, m.list))
	mux.HandleFunc("GET /api/goals/{goalId}", reportercontract.WithHeader(reportercontract.Goals, m.get))
	mux.HandleFunc("GET /api/goals/{goalId}/history", reportercontract.WithHeader(reportercontract.Goals, m.history))
	mux.HandleFunc("POST /api/goals", reportercontract.WithHeader(reportercontract.Goals, m.create))
	mux.HandleFunc("PATCH /api/goals/{goalId}", reportercontract.WithHeader(reportercontract.Goals, m.patch))
}

type apiFailure struct {
	status  int
	message string
}

func (e *apiFailure) Error() string         { return e.message }
func fail(status int, message string) error { return &apiFailure{status, message} }
func respond(w http.ResponseWriter, out any, err error) {
	w.Header().Set("Cache-Control", "no-store")
	if err == nil {
		httpapi.WriteJSON(w, 200, out)
		return
	}
	var f *apiFailure
	var v *ValidationError
	var pg *pgconn.PgError
	var size *http.MaxBytesError
	switch {
	case errors.As(err, &f):
		httpapi.WriteError(w, f.status, f.message)
	case errors.As(err, &size):
		httpapi.WriteError(w, 413, "body: exceeds 256 KiB")
	case errors.As(err, &v):
		httpapi.WriteError(w, 400, v.Error())
	case errors.Is(err, authz.ErrForbidden), errors.Is(err, db.ErrKeyAuthorityChanged):
		authz.WriteForbidden(w, err)
	case errors.Is(err, pgx.ErrNoRows):
		httpapi.WriteError(w, 404, "goal or outcome not found")
	case errors.As(err, &pg) && pg.Code == "23505":
		httpapi.WriteError(w, 409, "outcome ref or handed-over conflict")
	case errors.Is(err, context.DeadlineExceeded):
		httpapi.WriteError(w, 503, "goal request timed out")
	default:
		slog.Error("goals request", "error", err)
		httpapi.WriteError(w, 500, "internal")
	}
}
func requestPrincipal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, 401, "authentication required")
	}
	return p, ok
}
func (m *module) patch(w http.ResponseWriter, r *http.Request) {
	p, ok := requestPrincipal(w, r)
	if !ok {
		return
	}
	if !nodeIdentity(r.PathValue("goalId")) {
		respond(w, nil, invalid("goalId", "must be a goal key or UUID"))
		return
	}
	key, err := CleanIdempotencyKey(r.Header.Get("Idempotency-Key"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	// ReadRequestBody owns the transport deadline and the 256 KiB bound.
	if err = httpapi.BufferRequestBodyTimeout(w, r, MaxBodyBytes, 10*time.Second); err != nil {
		respond(w, nil, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	now := m.now()
	in, err := DecodeRequest(r.Body)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var out WriteResult
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var e error
		out, e = updateTx(ctx, tx, p, r.PathValue("goalId"), key, in, now)
		return e
	})
	respond(w, out, err)
}

type createInput struct {
	Title     string  `json:"title"`
	ProjectID *string `json:"project_id,omitempty"`
}
type WriteResult struct {
	ID      string `json:"id"`
	Key     string `json:"key"`
	Changed bool   `json:"changed"`
	Watermark
}

func (m *module) create(w http.ResponseWriter, r *http.Request) {
	p, ok := requestPrincipal(w, r)
	if !ok {
		return
	}
	key, err := CleanIdempotencyKey(r.Header.Get("Idempotency-Key"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	if err = httpapi.BufferRequestBodyTimeout(w, r, MaxBodyBytes, 10*time.Second); err != nil {
		respond(w, nil, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var in createInput
	if err = uniqueObjectKeys(raw); err == nil {
		err = decodeStrict(raw, &in)
	}
	if err == nil && (strings.TrimSpace(in.Title) == "" || utf8.RuneCountInString(in.Title) > 300) {
		err = invalid("title", "must contain 1–300 characters")
	}
	if err == nil && in.ProjectID != nil && !uuidPattern.MatchString(*in.ProjectID) {
		err = invalid("project_id", "must be a project UUID or null")
	}
	if err != nil {
		respond(w, nil, invalid("body", err.Error()))
		return
	}
	var out WriteResult
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error { var e error; out, e = createTx(ctx, tx, p, key, in); return e })
	respond(w, out, err)
}
