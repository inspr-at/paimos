// SPDX-License-Identifier: AGPL-3.0-only

package workorders

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Error is a safe HTTP failure shared by the work-order and run modules.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string              { return e.Message }
func Fail(status int, message string) error { return &Error{status, message} }

type orderPage struct {
	Items []Order
	Next  string
}

type orderCursor struct {
	Version int    `json:"v"`
	Tenant  string `json:"tenant"`
	ID      string `json:"id"`
}

func encodeCursor(tenantID, id string) string {
	raw, _ := json.Marshal(orderCursor{1, tenantID, id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(tenantID, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	var c orderCursor
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(b, &c) != nil || c.Version != 1 || c.Tenant != tenantID || !UUID(c.ID) {
		return "", Fail(400, "invalid cursor")
	}
	return c.ID, nil
}

// Endpoint wraps a tenant transaction. Responses are emitted only after commit.
// The server must install auth.Middleware; agent scopes are rechecked against
// the exact bearer key because R1 Principal does not carry key scopes.
func Endpoint(pool *pgxpool.Pool, scope string, agentOnly bool, status int, fn func(*http.Request, pgx.Tx, tenant.Principal) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		p, ok := tenant.PrincipalFrom(r.Context())
		if !ok || !UUID(p.ID) || !UUID(p.TenantID) {
			httpapi.WriteError(w, 401, "unauthorized")
			return
		}
		if (p.Kind != tenant.Person && p.Kind != tenant.Agent) || (agentOnly && p.Kind != tenant.Agent) {
			httpapi.WriteError(w, 403, "agent required")
			return
		}
		for _, key := range []string{"workOrderId", "criterionId", "runId"} {
			if id := r.PathValue(key); id != "" && !UUID(id) {
				httpapi.WriteError(w, 400, "invalid id")
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var result any
		err := db.InTenant(r.Context(), pool, p.TenantID, func(tx pgx.Tx) error {
			if workBindingMutation(r) {
				if err := db.LockWorkTreeTx(r.Context(), tx); err != nil {
					return err
				}
			}
			if err := authorizeKey(r, tx, p, scope); err != nil {
				return err
			}
			var err error
			result, err = fn(r, tx, p)
			return err
		})
		if err != nil {
			WriteError(w, err)
			return
		}
		// An explicit error response returned as the result is emitted after
		// commit, for mutations that release an obsolete hold before a conflict.
		if failure, ok := result.(*Error); ok {
			WriteError(w, failure)
			return
		}
		if page, ok := result.(orderPage); ok {
			if page.Next != "" {
				w.Header().Set("X-Next-Cursor", page.Next)
				u := *r.URL
				q := u.Query()
				q.Set("cursor", page.Next)
				u.RawQuery = q.Encode()
				w.Header().Set("Link", "<"+u.RequestURI()+">; rel=\"next\"")
			}
			result = page.Items
		}
		httpapi.WriteJSON(w, status, result)
	}
}

// Only admission/binding needs the tree fence. Heartbeats, telemetry and reads
// keep their existing pairing-only path and remain usable while the tree is busy.
func workBindingMutation(r *http.Request) bool {
	switch r.Pattern {
	case "POST /api/work-orders", "PATCH /api/work-orders/{workOrderId}",
		"POST /api/work-orders/{workOrderId}/runs", "POST /api/runs/{runId}/claim",
		"POST /api/projects/{projectId}/harness-sessions",
		"PATCH /api/projects/{projectId}/harness-sessions/{sessionId}/binding":
		return true
	}
	return false
}

func authorizeKey(r *http.Request, tx pgx.Tx, p tenant.Principal, scope string) error {
	if p.Kind == tenant.Person {
		return nil
	}
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return Fail(403, "scoped agent key required")
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(token), "aeon_")
	if !ok {
		return Fail(403, "scoped agent key required")
	}
	prefix, secret, ok := strings.Cut(rest, "_")
	if !ok {
		return Fail(403, "scoped agent key required")
	}
	sum := sha256.Sum256([]byte(secret))
	var allowed bool
	err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM agent_keys
	 WHERE tenant_id=$1 AND principal_id=$2 AND prefix=$3 AND hash=$4
	 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp())
	 AND $5=ANY(scopes))`, p.TenantID, p.ID, prefix, hex.EncodeToString(sum[:]), scope).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return Fail(403, "key scope required: "+scope)
	}
	return nil
}

func WriteError(w http.ResponseWriter, err error) {
	// Modules such as managed rules delivery carry a safe status and code.
	// Keep that structured failure when they run through this shared endpoint.
	var coded interface {
		error
		HTTPStatus() int
		ErrorCode() string
	}
	if errors.As(err, &coded) {
		httpapi.WriteJSON(w, coded.HTTPStatus(), map[string]string{"error": coded.Error(), "code": coded.ErrorCode()})
		return
	}
	if errors.Is(err, authz.ErrForbidden) {
		authz.WriteForbidden(w, err)
		return
	}
	var e *Error
	if errors.As(err, &e) {
		httpapi.WriteError(w, e.Status, e.Message)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.WriteError(w, 404, "not found")
		return
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		if pg.ConstraintName == "work_leaf_required" || pg.ConstraintName == "busy_work_leaf" || pg.ConstraintName == "work_handover_pending" {
			httpapi.WriteJSON(w, 409, map[string]string{"error": pg.Message, "code": pg.ConstraintName})
			return
		}
		switch pg.Code {
		case "23503", "23514", "22P02", "22003", "P0001":
			httpapi.WriteError(w, 400, "invalid value or related resource")
			return
		case "23505", "40001", "40P01":
			httpapi.WriteError(w, 409, "conflict; retry request")
			return
		}
	}
	httpapi.WriteError(w, 500, "internal")
}

// Decode accepts one bounded JSON object with no unknown fields.
func Decode(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return Fail(400, "invalid JSON")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Fail(400, "expected one JSON object")
	}
	return nil
}

func UUID(id string) bool {
	var u pgtype.UUID
	return len(id) == 36 && u.Scan(id) == nil && u.Valid
}

func Limit(r *http.Request) (int, error) {
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 200 {
			return 0, Fail(400, "limit must be 1..200")
		}
		return n, nil
	}
	return 50, nil
}
