// SPDX-License-Identifier: AGPL-3.0-only

package journey

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/reportercontract"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Module serves journey projection and action routes.
type Module struct {
	pool     *pgxpool.Pool
	inTenant func(context.Context, *pgxpool.Pool, string, func(pgx.Tx) error) error
}

var _ httpapi.Module = (*Module)(nil)

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func uuidOK(s string) bool { return uuidPattern.MatchString(s) }

// New returns the journey module. The coordinator mounts it; this package does
// not edit cmd/aeon. Routes, all under /api:
//
//	GET  /projects/{projectId}/journey
//	PUT  /projects/{projectId}/journey/profile
//	POST /projects/{projectId}/journey/actions
//
// Stage and the single next action are derived. GET also returns the project
// node's route key, its immutable node key and the tenant slug, read from those rows, so a
// consumer can verify its configured binding. GET is read-only; the first
// person action initializes a missing projection and records imported-stage
// derivation once. Human gates are live R2 approvals (journey.shape, the
// projection's revision-and-digest-bound requirements_approval_scope, journey.build,
// journey.candidate, journey.deploy, journey.access) on the project or release
// node. Actions never write an approval decision. Queries run inside db.InTenant
// and every mutation appends a tenant event in that transaction.
func New(pool *pgxpool.Pool) httpapi.Module {
	return &Module{pool: pool, inTenant: db.InTenant}
}

// Mount registers the journey routes.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/journey/next-actions", m.handleNextActions)
	mux.HandleFunc("GET /api/projects/{projectId}/journey", reportercontract.WithHeader(reportercontract.Journey, m.handleGet))
	mux.HandleFunc("PUT /api/projects/{projectId}/journey/profile", m.handleProfile)
	mux.HandleFunc("POST /api/projects/{projectId}/journey/actions", m.handleAction)
}

func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	out, err := m.read(r.Context(), p, id)
	writeResult(w, http.StatusOK, out, err)
}

func (m *Module) handleProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	var in profileWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	out, err := m.setProfile(r.Context(), p, id, in)
	writeResult(w, http.StatusOK, out, err)
}

func (m *Module) handleAction(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	var in actionWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	normalizeAction(&in)
	out, err := m.act(r.Context(), p, id, in)
	writeResult(w, http.StatusOK, out, err)
}

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !uuidOK(p.ID) || !uuidOK(p.TenantID) || (p.Kind != tenant.Person && p.Kind != tenant.Agent) {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return tenant.Principal{}, false
	}
	p.ID = strings.ToLower(p.ID)
	p.TenantID = strings.ToLower(p.TenantID)
	return p, true
}

func projectID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("projectId")
	if !uuidOK(id) {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return "", false
	}
	return strings.ToLower(id), true
}

func normalizeAction(in *actionWrite) {
	if in.ApprovalRequestID != nil && *in.ApprovalRequestID != "" {
		v := strings.ToLower(*in.ApprovalRequestID)
		in.ApprovalRequestID = &v
	}
	if in.ReleaseID != nil && *in.ReleaseID != "" {
		v := strings.ToLower(*in.ReleaseID)
		in.ReleaseID = &v
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	body, err := io.ReadAll(r.Body)
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		return fail(http.StatusBadRequest, "bad request")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fail(http.StatusBadRequest, "bad request")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fail(http.StatusBadRequest, "bad request")
	}
	return nil
}

func writeResult(w http.ResponseWriter, status int, v any, err error) {
	if err == nil {
		writeJSON(w, status, v)
		return
	}
	err = mapDB(err)
	var he *httpError
	if errors.As(err, &he) {
		writeError(w, he.status, he.msg)
		return
	}
	slog.Error("journey", "err", err)
	writeError(w, http.StatusInternalServerError, "internal")
}

func mapDB(err error) error {
	var he *httpError
	if errors.As(err, &he) {
		return err
	}
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return err
	}
	switch pe.Code {
	case "23505":
		return fail(http.StatusConflict, "journey conflict")
	case "23503":
		return fail(http.StatusForbidden, "approval does not grant this action")
	case "23514":
		return fail(http.StatusConflict, "invalid journey change")
	case "P0001":
		return fail(http.StatusConflict, "invalid journey change")
	default:
		return err
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteError(w, status, msg)
}
