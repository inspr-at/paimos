// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Module serves the intake routes.
type Module struct {
	pool      *pgxpool.Pool
	keys      *tokens.KeySet
	authority AuthorityStore
	clock     func() time.Time
}

var _ httpapi.Module = (*Module)(nil)

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// New returns an httpapi.Module for cited intake. Mount it from the coordinator.
func New(pool *pgxpool.Pool) httpapi.Module {
	return &Module{pool: pool}
}

// Mount registers the intake routes on mux.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/projects/{projectId}/intake/sources", m.delegated(m.addSource, scopeWrite))
	mux.HandleFunc("GET /api/projects/{projectId}/intake", m.delegated(m.getIntake, scopeRead))
	mux.HandleFunc("POST /api/projects/{projectId}/intake/transcript-turns", m.delegated(m.addTurn, scopeWrite))
	mux.HandleFunc("POST /api/projects/{projectId}/intake/drafts", m.delegated(m.proposeDraft, scopeWrite))
	mux.HandleFunc("POST /api/projects/{projectId}/intake/drafts/{draftId}/accept", m.delegated(m.acceptDraft, ""))
	mux.HandleFunc("POST /api/projects/{projectId}/intake/drafts/{draftId}/replace", m.delegated(m.replaceDraft, scopeWrite))
}

type httpError struct {
	status int
	msg    string
	code   string
}

func (e *httpError) Error() string { return e.msg }

func refusal(status int, code, msg string) error {
	return &httpError{status: status, code: code, msg: msg}
}

func fail(status int, msg string) error {
	code := "invalid_request"
	switch status {
	case http.StatusUnauthorized:
		code = "unauthenticated"
	case http.StatusForbidden:
		code = "forbidden"
	case http.StatusNotFound:
		code = "not_found"
	case http.StatusConflict:
		code = "conflict"
	case http.StatusRequestEntityTooLarge:
		code = "too_large"
	case http.StatusServiceUnavailable:
		code = "unavailable"
	case http.StatusInternalServerError:
		code = "internal"
	}
	return refusal(status, code, msg)
}

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !uuidPattern.MatchString(p.ID) || !uuidPattern.MatchString(p.TenantID) {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return tenant.Principal{}, false
	}
	if p.Kind != tenant.Person && p.Kind != tenant.Agent {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return tenant.Principal{}, false
	}
	return p, true
}

func pathUUID(w http.ResponseWriter, raw string) (string, bool) {
	if !uuidPattern.MatchString(raw) {
		writeError(w, http.StatusNotFound, "not found")
		return "", false
	}
	return strings.ToLower(raw), true
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, status, "invalid JSON")
		return false
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Cache-Control", "no-store")
	he := fail(status, msg).(*httpError)
	writeCode(w, status, he.code, msg)
}

func writeCode(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, map[string]string{"error": msg, "code": code})
}

func writeResult(w http.ResponseWriter, status int, v any, err error) {
	if err == nil {
		writeJSON(w, status, v)
		return
	}
	var he *httpError
	if errors.As(err, &he) {
		writeCode(w, he.status, he.code, he.msg)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505":
			writeError(w, http.StatusConflict, "conflict")
			return
		case "23514", "23503", "22P02", "22003":
			writeError(w, http.StatusBadRequest, "invalid value")
			return
		case "P0001":
			if pe.Message == "already_accepted" || pe.Message == "draft_superseded" {
				writeCode(w, http.StatusConflict, pe.Message, pe.Message)
				return
			}
			writeError(w, http.StatusConflict, "conflict")
			return
		}
	}
	slog.Error("intake", "err", err)
	writeError(w, http.StatusInternalServerError, "internal")
}

func authorize(ctx context.Context, r *http.Request, tx pgx.Tx, p tenant.Principal, write bool) ([]string, error) {
	if c, ok := delegatedClaims(ctx); ok {
		want := scopeRead
		if write {
			want = scopeWrite
		}
		if !contains(c.Capabilities, want) {
			return nil, fail(http.StatusForbidden, "exact delegated capability required")
		}
		return c.Capabilities, nil
	}
	if p.Kind == tenant.Person {
		return nil, nil
	}
	scopes, err := keyScopes(ctx, r, tx, p)
	if err != nil {
		return nil, err
	}
	if write {
		if !contains(scopes, scopeWrite) {
			return nil, fail(http.StatusForbidden, "key scope required: "+scopeWrite)
		}
		return scopes, nil
	}
	if contains(scopes, scopeRead) || contains(scopes, scopeWrite) {
		return scopes, nil
	}
	return nil, fail(http.StatusForbidden, "key scope required: "+scopeRead)
}

func keyScopes(ctx context.Context, r *http.Request, tx pgx.Tx, p tenant.Principal) ([]string, error) {
	prefix, secret, ok := splitBearer(r.Header.Get("Authorization"))
	if !ok || !prefixMatchesTenant(prefix, p.TenantID) {
		return nil, fail(http.StatusForbidden, "scoped agent key required")
	}
	sum := sha256.Sum256([]byte(secret))
	var scopes pgtype.FlatArray[string]
	var fullAccess bool
	err := tx.QueryRow(ctx, `
		SELECT scopes,coalesce(full_access,false) FROM agent_keys
		WHERE tenant_id = $1::uuid AND principal_id = $2::uuid AND prefix = $3 AND hash = $4
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())`,
		p.TenantID, p.ID, prefix, hex.EncodeToString(sum[:])).Scan(&scopes, &fullAccess)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fail(http.StatusForbidden, "scoped agent key required")
	}
	if err != nil {
		return nil, err
	}
	return authz.ResolveKeyScopes([]string(scopes), fullAccess), nil
}

func splitBearer(header string) (prefix, secret string, ok bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", "", false
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(token), "aeon_")
	if !ok {
		return "", "", false
	}
	prefix, secret, ok = strings.Cut(rest, "_")
	if !ok || prefix == "" || secret == "" || strings.Contains(secret, "_") {
		return "", "", false
	}
	return prefix, secret, true
}

func prefixMatchesTenant(prefix, tenantID string) bool {
	compact := strings.ReplaceAll(strings.ToLower(tenantID), "-", "")
	return len(compact) == 32 && len(prefix) >= 48 && strings.EqualFold(prefix[:32], compact)
}

func contains(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

func (m *Module) tx(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	return db.InTenant(ctx, m.pool, tenantID, fn)
}
