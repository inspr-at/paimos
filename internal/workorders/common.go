// SPDX-License-Identifier: AGPL-3.0-only

package workorders

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	return EndpointPrepared(pool, scope, agentOnly, status, nil, fn)
}

// EndpointPrepared decodes/prepares before acquiring the final transaction's
// fences. The request shares one deadline across both phases.
func EndpointPrepared(pool *pgxpool.Pool, scope string, agentOnly bool, status int, prepare func(*http.Request, tenant.Principal) (*http.Request, error), fn func(*http.Request, pgx.Tx, tenant.Principal) (any, error)) http.HandlerFunc {
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
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		// Ordinary handlers decode inside fn; replay bounded memory there,
		// never transport input while holding tenant/tree/resource fences.
		if err := BufferEndpointBody(w, r); err != nil {
			WriteError(w, err)
			return
		}
		if prepare != nil {
			var err error
			r, err = prepare(r, p)
			if err != nil {
				WriteError(w, err)
				return
			}
		}
		var result any
		err := db.InTenant(r.Context(), pool, p.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(r.Context(), `SELECT set_config('lock_timeout','5s',true),set_config('statement_timeout','5s',true)`); err != nil {
				return err
			}
			if err := db.LockTenant(r.Context(), tx, p.TenantID); err != nil {
				return err
			}
			if workBindingMutation(r) {
				if err := db.LockWorkTreeTx(r.Context(), tx); err != nil {
					return err
				}
			}
			if prepare != nil {
				// Decoded composite entries sample exact-key expiry only after
				// their shared tree fence, before target/order/run/account work.
				if err := db.LockTree(r.Context(), tx, p.TenantID); err != nil {
					return err
				}
			}
			current, err := CurrentKeyPrincipal(r, tx, p, scope)
			if err != nil {
				return err
			}
			if prepare != nil {
				p = current
				if err := RefreshPrincipal(r.Context(), tx, p); err != nil {
					return err
				}
			}
			// Ordinary endpoints retain their authenticated caller's narrowed
			// principal and visibility until their own final authorization check.
			// Replacing it with the bearer row can widen secondary permissions or
			// hide the target before that check reports a revoked grant.
			result, err = fn(r, tx, p)
			return err
		})
		if err != nil {
			WriteError(w, err)
			return
		}
		// An explicit error response returned as the result is emitted after
		// commit, for mutations that release an obsolete hold before a conflict.
		if failure, ok := result.(error); ok {
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

// BufferBody reads at most 1 MiB under a ten-second connection deadline before
// transaction admission. Failed reads retain the deadline for net/http's drain;
// successful reads replace the body with memory and clear the deadline.
func BufferBody(w http.ResponseWriter, r *http.Request) error {
	if r.Body == nil || r.Body == http.NoBody {
		r.Body = http.NoBody
		return nil
	}
	controller := http.NewResponseController(w)
	// net/http supports this through the production middleware's Unwrap.
	// In-memory response recorders used by handler tests have no connection.
	if err := controller.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		// Keep the deadline on failed reads so net/http cannot drain a stalled
		// body indefinitely after the handler returns.
		var timeout interface{ Timeout() bool }
		if errors.As(err, &timeout) && timeout.Timeout() {
			return Fail(http.StatusRequestTimeout, "request body read timed out")
		}
		return Fail(http.StatusBadRequest, "invalid JSON")
	}
	_ = r.Body.Close()
	_ = controller.SetReadDeadline(time.Time{})
	r.Body = io.NopCloser(bytes.NewReader(body))
	return nil
}

// Admission/binding and generation recovery take the tree fence before any
// resource rows. A heartbeat can revive a generation, and resume can register
// its successor; both must use this protocol with the status rollout disabled.
func workBindingMutation(r *http.Request) bool {
	switch r.Pattern {
	case "POST /api/work-orders", "PATCH /api/work-orders/{workOrderId}",
		"POST /api/work-orders/{workOrderId}/runs", "POST /api/runs/{runId}/claim",
		"POST /api/projects/{projectId}/harness-sessions",
		"PATCH /api/projects/{projectId}/harness-sessions/{sessionId}/binding",
		"POST /api/projects/{projectId}/harness-sessions/{sessionId}/heartbeat",
		"POST /api/projects/{projectId}/harness-sessions/{sessionId}/confirm-exit",
		"PUT /api/nodes/{nodeId}/live-eta",
		"POST /api/projects/{projectId}/harness-sessions/{sessionId}/resume":
		return true
	}
	return false
}

// bodyInterruptGrace bounds how long a cancelled request lets an in-flight
// transport read return on its own before the connection deadline is moved into
// the past. net/http cancels the request context from inside a failing read
// (connReader.handleReadErrorLocked), so a cancelled context does not prove a
// stalled read; the read that just timed out delivers its result within
// microseconds. Interrupting it anyway would issue a second deadline call on a
// connection whose post-handler drain already relies on the first one.
const bodyInterruptGrace = time.Second

type bodyResult struct {
	raw []byte
	err error
}

// BufferEndpointBody finishes all network reads before preparation or a database
// transaction. A transport deadline interrupts HTTP reads; the context bound
// also protects callers whose ResponseWriter cannot set transport deadlines.
// Callers must install a bounded request context before entering this helper.
//
// A failed read keeps its transport deadline (AEON-652): net/http drains the
// unread body after the handler returns, and clearing the deadline would let
// a stalled client hold that drain indefinitely. Exactly one deadline call is
// made when the transport ends the read itself; only a read that is still
// blocked after cancellation is interrupted with an already expired deadline.
func BufferEndpointBody(w http.ResponseWriter, r *http.Request) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	controller := http.NewResponseController(w)
	transport := false
	if deadline, ok := r.Context().Deadline(); ok {
		transport = controller.SetReadDeadline(deadline) == nil
	}
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	done := make(chan bodyResult, 1)
	go func() {
		raw, err := io.ReadAll(body)
		done <- bodyResult{raw, err}
	}()
	var got bodyResult
	select {
	case got = <-done:
	case <-r.Context().Done():
		got = interruptBody(controller, body, done, transport)
	}
	if got.err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(got.err, &tooLarge) {
			return Fail(http.StatusRequestEntityTooLarge, "request body too large")
		}
		var timedOut net.Error
		if r.Context().Err() != nil || errors.As(got.err, &timedOut) && timedOut.Timeout() {
			return Fail(http.StatusRequestTimeout, "request body read timed out")
		}
		return Fail(http.StatusBadRequest, "could not read request body")
	}
	if r.Context().Err() != nil {
		return Fail(http.StatusRequestTimeout, "request body read timed out")
	}
	_ = r.Body.Close()
	if transport {
		// Only a complete body resumes keep-alive reads without a deadline.
		_ = controller.SetReadDeadline(time.Time{})
	}
	r.Body = io.NopCloser(bytes.NewReader(got.raw))
	return nil
}

// interruptBody ends a read whose request context is already done. With a
// transport deadline installed, a read that failed on its own (and thereby
// cancelled the context) is joined without touching the connection again; a
// read still blocked after the grace period gets an expired deadline. Close
// releases cancellable bodies without a transport; HTTP bodies must unblock
// their concurrent Read before the response is written.
func interruptBody(controller *http.ResponseController, body io.ReadCloser, done <-chan bodyResult, transport bool) bodyResult {
	if transport {
		timer := time.NewTimer(bodyInterruptGrace)
		defer timer.Stop()
		select {
		case got := <-done:
			return got
		case <-timer.C:
		}
		_ = controller.SetReadDeadline(time.Unix(1, 0))
	}
	_ = body.Close()
	return <-done
}

// CurrentKeyPrincipal repeats the exact initiating key ceiling under the caller's
// tenant fence, including current scopes and creator. It never logs key material.
func CurrentKeyPrincipal(r *http.Request, tx pgx.Tx, p tenant.Principal, scope string) (tenant.Principal, error) {
	if p.Kind == tenant.Person {
		return p, nil
	}
	var now time.Time
	if err := tx.QueryRow(r.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return p, err
	}
	return CurrentKeyPrincipalAt(r, tx, p, scope, now)
}

// CurrentKeyPrincipalAt accepts a post-fence database clock sample. Preparation
// may inject that sample for deterministic expiry-boundary tests.
func CurrentKeyPrincipalAt(r *http.Request, tx pgx.Tx, p tenant.Principal, scope string, now time.Time) (tenant.Principal, error) {
	if p.Kind == tenant.Person {
		return p, nil
	}
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return p, Fail(403, "scoped agent key required")
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(token), "aeon_")
	if !ok || len(rest) > 512 {
		return p, Fail(403, "scoped agent key required")
	}
	prefix, secret, ok := strings.Cut(rest, "_")
	if !ok {
		return p, Fail(403, "scoped agent key required")
	}
	sum := sha256.Sum256([]byte(secret))
	var creator *string
	err := tx.QueryRow(r.Context(), `SELECT scopes,created_by_principal_id::text,coalesce(full_access,false) FROM agent_keys
 WHERE tenant_id=$1 AND principal_id=$2 AND prefix=$3 AND hash=$4
 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>$5)`, p.TenantID, p.ID, prefix, hex.EncodeToString(sum[:]), now).Scan(&p.Scopes, &creator, &p.FullAccess)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, Fail(403, "key scope required: "+scope)
	}
	if err != nil {
		return p, err
	}
	p.Scopes = authz.ResolveKeyScopes(p.Scopes, p.FullAccess)
	allowed := false
	for _, s := range p.Scopes {
		if s == scope || scope == "models.read" && strings.ReplaceAll(s, ":", ".") == scope {
			allowed = true
		}
	}
	// Historical coordinator keys have the established read-only ceiling.
	// Re-evaluate it from this exact key's current stored scopes; RequireTx
	// still requires current principal and creator bindings. Other operations
	// retain their original exact scope requirement.
	if scope == "models.read" && authz.CoordinatorCeiling(p.Scopes, scope) {
		allowed = true
	}
	if !allowed {
		return p, Fail(403, "key scope required: "+scope)
	}
	p.KeyCreatorID = ""
	if creator != nil {
		p.KeyCreatorID = *creator
	}
	return p, nil
}

// RefreshPrincipal re-derives RLS visibility after the access fence. Cached
// middleware placement/creator authority cannot select the preparation scope.
func RefreshPrincipal(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	var creator any
	if UUID(p.KeyCreatorID) {
		creator = p.KeyCreatorID
	}
	_, err := tx.Exec(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid)`, p.TenantID, p.ID, creator)
	return err
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
