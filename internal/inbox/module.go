// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/attachedmsg"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

const scopeInboxSend = "inbox.send"

type module struct {
	attached   *attachedmsg.Service
	pool       *pgxpool.Pool
	heartbeat  time.Duration
	waitNotify func(context.Context, *pgx.Conn, string, time.Time) error
}

// New returns the inbox HTTP module. See the package doc for coordinator wiring.
func New(pool *pgxpool.Pool, attached ...*attachedmsg.Service) httpapi.Module {
	return newModule(pool, attached...)
}

func newModule(pool *pgxpool.Pool, attached ...*attachedmsg.Service) *module {
	var service *attachedmsg.Service
	if len(attached) > 0 {
		service = attached[0]
	}
	return &module{pool: pool, heartbeat: 15 * time.Second, waitNotify: waitTenantNotify, attached: service}
}

func (m *module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/inbox/messages", m.listen)
	mux.HandleFunc("POST /api/inbox/messages", m.handleSend)
	mux.HandleFunc("GET /api/inbox/stream", m.stream)
	mux.HandleFunc("GET /api/inbox/messages/{messageId}/receipt", m.handleReceipt)
	mux.HandleFunc("POST /api/inbox/messages/{messageId}/ack", m.handleAck)
	mux.HandleFunc("POST /api/inbox/messages/{messageId}/cancel", m.cancelAttached)
	mux.HandleFunc("GET /api/inbox/targets", m.handleListTargets)
	mux.HandleFunc("POST /api/inbox/targets", m.handleCreateTarget)
	mux.HandleFunc("DELETE /api/inbox/targets/{targetId}", m.handleDeleteTarget)
	mux.HandleFunc("GET /api/inbox/message-status", m.handleMessageStatus)
	mux.HandleFunc("GET /api/inbox/feedback-recipient", m.handleFeedbackRecipient)
	mux.HandleFunc("GET /api/settings/inbox-delivery", m.handleGetDeliverySettings)
	mux.HandleFunc("PUT /api/settings/inbox-delivery", m.handlePutDeliverySettings)
}

type httpError struct {
	status int
	code   string
	msg    string
}

func (e *httpError) Error() string { return e.msg }

var (
	errForbidden = &httpError{403, "forbidden", "forbidden"}
	errNotFound  = &httpError{404, "not_found", "not found"}
	errConflict  = &httpError{409, "conflict", "idempotency key was already used with a different message"}
)

func badRequest(msg string) *httpError {
	return &httpError{400, "invalid_request", msg}
}

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return tenant.Principal{}, false
	}
	return p, true
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, v)
}

func failure(w http.ResponseWriter, err error) {
	if attachedmsg.WriteError(w, err) {
		return
	}
	var he *httpError
	if errors.As(err, &he) {
		writeError(w, he.status, he.code, he.msg)
		return
	}
	var pe *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, 404, "not_found", "not found")
	case errors.As(err, &pe) && pe.Code == "23505":
		writeError(w, 409, "conflict", "inbox change conflicts with an existing row")
	case errors.As(err, &pe) && pe.Code == "23503":
		writeError(w, 404, "not_found", "not found")
	case errors.As(err, &pe) && pe.Code == "23514":
		writeError(w, 400, "invalid_request", "message violates inbox constraints")
	case errors.As(err, &pe) && pe.Code == "P0001":
		writeError(w, 409, "conflict", "wake target does not belong to the recipient")
	default:
		writeError(w, 500, "internal_error", "internal error")
	}
}

func mapWrite(err error) error {
	if err == nil {
		return nil
	}
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
		return &httpError{409, "conflict", "inbox change conflicts with an existing row"}
	case "23503":
		return errNotFound
	case "23514":
		return badRequest("message violates inbox constraints")
	case "P0001":
		return &httpError{409, "conflict", "wake target does not belong to the recipient"}
	default:
		return err
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, 400, "invalid_request", "invalid JSON body")
		return false
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeError(w, 400, "invalid_request", "invalid JSON body")
		return false
	}
	return true
}

func parseUUID(s string) (string, bool) {
	var u pgtype.UUID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' || u.Scan(s) != nil || !u.Valid {
		return "", false
	}
	return strings.ToLower(s), true
}

func parseNonNeg(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid")
		}
	}
	return strconv.ParseInt(s, 10, 64)
}

// authorizeSend enforces inbox.send for agents. The principal carries no
// scopes, so the bearer token is resolved against agent_keys in this tenant.
// A person session is not an agent key and is allowed through.
func (m *module) authorizeSend(ctx context.Context, r *http.Request, p tenant.Principal) error {
	if p.Kind == tenant.Person {
		return nil
	}
	if p.Kind != tenant.Agent {
		return errForbidden
	}
	prefix, secret, ok := splitBearer(r.Header.Get("Authorization"))
	if !ok || !prefixMatchesTenant(prefix, p.TenantID) {
		return errForbidden
	}
	sum := sha256.Sum256([]byte(secret))
	hash := hex.EncodeToString(sum[:])
	var scopes []string
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT scopes FROM agent_keys
			WHERE prefix = $1 AND hash = $2 AND principal_id = $3::uuid
			  AND revoked_at IS NULL
			  AND (expires_at IS NULL OR expires_at > now())`,
			prefix, hash, p.ID).Scan(&scopes)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return errForbidden
	}
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		if scope == scopeInboxSend {
			return nil
		}
	}
	return errForbidden
}

func splitBearer(h string) (prefix, secret string, ok bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(h), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", "", false
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(token), "aeon_")
	if !ok {
		return "", "", false
	}
	prefix, secret, ok = strings.Cut(rest, "_")
	if !ok || prefix == "" || secret == "" {
		return "", "", false
	}
	return prefix, secret, true
}

func prefixMatchesTenant(prefix, tenantID string) bool {
	compact := strings.ReplaceAll(strings.ToLower(tenantID), "-", "")
	if len(compact) != 32 || len(prefix) < 48 {
		return false
	}
	for _, c := range prefix[:32] {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return strings.EqualFold(prefix[:32], compact)
}
