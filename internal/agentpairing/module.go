// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
	"github.com/inspr-at/paimos/internal/version"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Module struct {
	pool                  *pgxpool.Pool
	origin, defaultTenant string
	mu                    sync.Mutex
	clients               map[string]rate
	watch                 watchRelay
	watchKeys             watchPollKeys
}
type rate struct {
	start time.Time
	n     int
}

func New(pool *pgxpool.Pool, publicURL, defaultTenant string) *Module {
	origin := ""
	if u, err := url.Parse(publicURL); err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") {
		origin = u.Scheme + "://" + u.Host
	}
	return &Module{pool: pool, origin: origin, defaultTenant: defaultTenant, clients: map[string]rate{}}
}
func (m *Module) Mount(mux *http.ServeMux) {
	m.mountWatch(mux)
	mux.HandleFunc("GET /api/agent-pairing/guide", m.guide)
	mux.HandleFunc("POST /api/agent-pairing/device", m.device)
	mux.HandleFunc("POST /api/agent-pairing/redeem", m.redeem)
	mux.HandleFunc("POST /api/agent-pairing/reconcile", m.reconcile)
	mux.HandleFunc("POST /api/agent-pairing/lookup", m.person("account.manage", m.lookup))
	mux.HandleFunc("POST /api/agent-pairing/requests/{requestId}/approve", m.person("account.manage", m.approve))
	mux.HandleFunc("POST /api/agent-pairing/requests/{requestId}/deny", m.person("account.manage", m.deny))
	mux.HandleFunc("GET /api/agent-pairing/computers", m.person("account.read", m.list))
	mux.HandleFunc("GET /api/agent-pairing/computers/{computerId}", m.person("account.read", m.get))
	mux.HandleFunc("POST /api/agent-pairing/computers/{computerId}/disconnect", m.person("account.manage", m.disconnect))
	mux.HandleFunc("POST /api/agent-pairing/computers/{computerId}/enrollments/{accountId}/disconnect", m.person("account.manage", m.disconnect))
	mux.HandleFunc("GET /api/agent-pairing/self", m.self)
	mux.HandleFunc("POST /api/agent-pairing/self/disconnect", m.self)
}

// Both auth and route registry use this exact method/path allowlist, never a
// prefix exemption that could accidentally expose a later privileged route.
func PublicRoute(method, path string) bool {
	return method == "GET" && path == "/api/agent-pairing/guide" || method == "POST" && (path == "/api/agent-pairing/device" || path == "/api/agent-pairing/redeem" || path == "/api/agent-pairing/reconcile")
}
func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, v)
}
func (m *Module) guide(w http.ResponseWriter, r *http.Request) {
	reply(w, map[string]any{"protocol": "pairing-v1", "version": version.Version, "verification_helper_version": version.Version, "verification_capabilities": verificationCapabilities("", ""), "instance_url": m.origin, "default_tenant_slug": m.defaultTenant, "platforms": []string{"darwin/arm64", "darwin/amd64", "linux/arm64", "linux/amd64"}, "setup_command": setupCommand(m.origin), "platform_qualification": "candidate; consult the exact release service qualification evidence", "install_targets": installTargets(), "install_available": len(installTargets()) > 0, "managed_installation": "Use the owning Nix/Home Manager configuration; do not overwrite managed binaries or services"})
}

// Deliberately ignore X-Forwarded-For. The transport peer bounds memory/traffic;
// persistent per-tenant counters below enforce a multi-process attempt limit.
func (m *Module) throttle(r *http.Request) bool {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for k, v := range m.clients {
		if now.Sub(v.start) > time.Minute {
			delete(m.clients, k)
		}
	}
	v, ok := m.clients[peer]
	if !ok && len(m.clients) >= 4096 {
		return false
	}
	if v.start.IsZero() {
		v.start = now
	}
	v.n++
	m.clients[peer] = v
	return v.n <= 120
}
func (m *Module) public(w http.ResponseWriter, r *http.Request) bool {
	// A logged-in browser or another runtime bearer cannot turn a public proof
	// endpoint into an alternate person/agent authority path.
	if r.Header.Get("Authorization") != "" || r.Header.Get("Origin") != "" && r.Header.Get("Origin") != m.origin {
		WriteError(w, fail(403, "forbidden", "local proof request required"))
		return false
	}
	if !m.throttle(r) {
		WriteError(w, fail(429, "rate_limited", "pairing attempt limit reached"))
		return false
	}
	return true
}
func (m *Module) person(permission string, fn func(http.ResponseWriter, *http.Request, tenant.Principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := tenant.PrincipalFrom(r.Context())
		if !ok || p.Kind != tenant.Person || authz.Require(authz.BindPool(r.Context(), m.pool), permission, authz.Scope{}) != nil {
			WriteError(w, fail(403, "forbidden", "signed-in person permission required"))
			return
		}
		if r.Method != "GET" && (m.origin == "" || r.Header.Get("Origin") != m.origin || r.Header.Get("Sec-Fetch-Site") == "cross-site") {
			WriteError(w, fail(403, "forbidden", "same-origin approval required"))
			return
		}
		fn(w, r, p)
	}
}
func (m *Module) in(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	return db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		if err := Lock(ctx, tx); err != nil {
			return err
		}
		return fn(tx)
	})
}

// Count attempts in its own committed transaction, even when the operation
// subsequently fails. Only three bounded rows per tenant are ever allocated.
func (m *Module) limit(ctx context.Context, tenantID, bucket string, max int) error {
	var count int
	err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO agent_pairing_limits(tenant_id,bucket,attempts) VALUES($1,$2,1)
   ON CONFLICT(tenant_id,bucket) DO UPDATE SET
    attempts=CASE WHEN agent_pairing_limits.starts_at<clock_timestamp()-interval '10 minutes' THEN 1 ELSE agent_pairing_limits.attempts+1 END,
    starts_at=CASE WHEN agent_pairing_limits.starts_at<clock_timestamp()-interval '10 minutes' THEN clock_timestamp() ELSE agent_pairing_limits.starts_at END
   RETURNING attempts`, tenantID, bucket).Scan(&count)
	})
	if err != nil {
		return fail(404, "not_found", "tenant not found")
	}
	if count > max {
		return fail(429, "rate_limited", "pairing attempt limit reached")
	}
	return nil
}
func (m *Module) device(w http.ResponseWriter, r *http.Request) {
	if !m.public(w, r) {
		return
	}
	var in deviceRequest
	if err := decode(w, r, &in); err != nil {
		WriteError(w, err)
		return
	}
	if err := validateDevice(in); err != nil {
		WriteError(w, err)
		return
	}
	if in.TenantSlug != "" {
		var err error
		in.TenantID, err = tenantbootstrap.ResolveSlug(r.Context(), m.pool, in.TenantSlug)
		if err != nil {
			WriteError(w, fail(404, "not_found", "tenant not found"))
			return
		}
	}
	if err := m.limit(r.Context(), in.TenantID, "device", 60); err != nil {
		WriteError(w, err)
		return
	}
	var rec record
	err := m.in(r.Context(), in.TenantID, func(tx pgx.Tx) error {
		var err error
		rec, err = load(r.Context(), tx, in.RequestID)
		if err == nil {
			// Retries compare original client selections; a omitted model was filled
			// by the server once and remains pinned on every retry.
			old := rec.Details
			for i := range in.Accounts {
				if i < len(old.Accounts) && in.Accounts[i].ProfileID == "" {
					in.Accounts[i].ProfileID = old.Accounts[i].ProfileID
				}
			}
			b, _ := json.Marshal(in.Details)
			o, _ := json.Marshal(old)
			if string(b) != string(o) || rec.DeviceHash != in.DeviceHash || rec.RuntimeHash != in.RuntimeHash || rec.LifecycleHash != in.LifecycleHash {
				return fail(409, "conflict", "request ID is bound to different details")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if in.ExistingComputerID != "" {
			var h, state, runtime string
			var raw []byte
			err = tx.QueryRow(r.Context(), `SELECT c.lifecycle_hash,c.state,q.runtime_hash,q.details FROM agent_pairing_computers c JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id WHERE c.id=$1`, in.ExistingComputerID).Scan(&h, &state, &runtime, &raw)
			if err != nil || subtle.ConstantTimeCompare([]byte(h), []byte(digest(in.ExistingProof))) != 1 {
				return fail(404, "not_found", "computer proof not found")
			}
			var original Details
			if err = json.Unmarshal(raw, &original); err != nil {
				return err
			}
			if state != "connected" {
				return fail(409, "pairing_revoked", "computer is disconnecting or revoked; pair afresh")
			}
			if in.RuntimeHash != runtime || in.LifecycleHash != h || in.Workspace != original.Workspace || in.Platform != original.Platform || in.Arch != original.Arch || in.ComputerName != original.ComputerName {
				return fail(409, "conflict", "existing computer binding must remain unchanged")
			}
		}
		for i := range in.Accounts {
			a := &in.Accounts[i]
			if a.ProfileID == "" {
				err = tx.QueryRow(r.Context(), `SELECT id::text FROM model_profiles WHERE harness=$1 AND enabled ORDER BY created_at DESC,id LIMIT 1`, a.Harness).Scan(&a.ProfileID)
			} else {
				var id string
				err = tx.QueryRow(r.Context(), `SELECT id::text FROM model_profiles WHERE id=$1 AND harness=$2 AND enabled`, a.ProfileID, a.Harness).Scan(&id)
			}
			if errors.Is(err, pgx.ErrNoRows) {
				return fail(400, "invalid_request", "selected harness needs an enabled tenant model profile")
			}
			if err != nil {
				return err
			}
		}
		body, _ := json.Marshal(in.Details)
		// Code is display-only; its match never grants redemption authority.
		var code string
		for i := 0; i < 10; i++ {
			random, err := randomHex(8)
			if err != nil {
				return err
			}
			var n uint64
			_, err = fmt.Sscanf(random, "%x", &n)
			if err != nil {
				return err
			}
			code = fmt.Sprintf("%09d", n%1000000000)
			var exists bool
			if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_pairing_requests WHERE user_code=$1)`, code).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				break
			}
			code = ""
		}
		if code == "" {
			return fail(429, "rate_limited", "try again later")
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, in.TenantID, in.RequestID, code, in.DeviceHash, in.RuntimeHash, in.LifecycleHash, body, digest(string(body)))
		if err != nil {
			return err
		}
		rec, err = load(r.Context(), tx, in.RequestID)
		return err
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, map[string]any{"request_id": rec.ID, "tenant_id": rec.TenantID, "user_code": rec.Code[:3] + "-" + rec.Code[3:6] + "-" + rec.Code[6:], "state": rec.State, "expires_at": rec.ExpiresAt, "interval_seconds": 5, "verification_uri": m.origin + "/agents/register-agent", "request_digest": rec.Digest})
}
func load(ctx context.Context, tx pgx.Tx, id string) (record, error) {
	var v record
	var raw []byte
	if !uuidRE.MatchString(id) {
		return v, pgx.ErrNoRows
	}
	err := tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,verification,expires_at,verification_expires_at,approved_by::text,computer_id::text,attempts FROM agent_pairing_requests WHERE id=$1 FOR UPDATE`, id).Scan(&v.ID, &v.TenantID, &v.Code, &v.DeviceHash, &v.RuntimeHash, &v.LifecycleHash, &raw, &v.Digest, &v.State, &v.Mode, &v.ExpiresAt, &v.VerificationExpiresAt, &v.ApprovedBy, &v.ComputerID, &v.Attempts)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(raw, &v.Details)
	return v, err
}
func (m *Module) lookup(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var in struct {
		Code string `json:"user_code"`
	}
	if err := decode(w, r, &in); err != nil {
		WriteError(w, err)
		return
	}
	if err := m.limit(r.Context(), p.TenantID, "lookup", 30); err != nil {
		WriteError(w, err)
		return
	}
	code := strings.ReplaceAll(in.Code, "-", "")
	if len(code) != 9 {
		WriteError(w, fail(404, "not_found", "pairing code not found"))
		return
	}
	var v View
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(r.Context(), `SELECT id::text FROM agent_pairing_requests WHERE user_code=$1`, code).Scan(&id); err != nil {
			return notFound(err)
		}
		rec, err := load(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if err = expire(r.Context(), tx, &rec); err != nil {
			return err
		}
		v, err = view(r.Context(), tx, rec, false)
		return err
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, v)
}
func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(404, "not_found", "pairing not found")
	}
	return err
}
func (m *Module) redeem(w http.ResponseWriter, r *http.Request)    { m.proof(w, r, false) }
func (m *Module) reconcile(w http.ResponseWriter, r *http.Request) { m.proof(w, r, true) }
func (m *Module) proof(w http.ResponseWriter, r *http.Request, reconcile bool) {
	if !m.public(w, r) {
		return
	}
	var in proofRequest
	if err := decode(w, r, &in); err != nil {
		WriteError(w, err)
		return
	}
	secret := in.DeviceSecret
	if reconcile {
		secret = in.LifecycleSecret
	}
	if !uuidRE.MatchString(in.TenantID) || !uuidRE.MatchString(in.RequestID) || !hashRE.MatchString(secret) || len(in.Cleaned) > 256 || reconcile && in.DeviceSecret != "" || !reconcile && (in.LifecycleSecret != "" || len(in.Cleaned) > 0 || in.ComputerCleaned || in.Progress != nil) {
		WriteError(w, fail(400, "invalid_request", "invalid proof request"))
		return
	}
	if err := m.limit(r.Context(), in.TenantID, "proof", 2400); err != nil {
		WriteError(w, err)
		return
	}
	var v View
	var resultErr error
	// Invalid proofs commit their bounded attempts instead of rolling back.
	// This proof handler touches only the verified computer and workspace audit; no project visibility is granted.
	err := m.in(db.NoProjects(r.Context(), "pairing lifecycle proof"), in.TenantID, func(tx pgx.Tx) error {
		rec, err := load(r.Context(), tx, in.RequestID)
		if err != nil {
			return notFound(err)
		}
		expected := rec.DeviceHash
		if reconcile {
			expected = rec.LifecycleHash
		}
		if subtle.ConstantTimeCompare([]byte(digest(secret)), []byte(expected)) != 1 {
			if !reconcile && rec.Attempts < 10 {
				_, err = tx.Exec(r.Context(), `UPDATE agent_pairing_requests SET attempts=attempts+1 WHERE id=$1`, rec.ID)
			}
			resultErr = fail(404, "not_found", "pairing proof not found")
			return err
		}
		if err = expire(r.Context(), tx, &rec); err != nil {
			return err
		}
		if reconcile {
			if rec.ComputerID == nil {
				return fail(409, "conflict", "computer is not enrolled")
			}
			if err = finalizeDrain(r.Context(), tx, *rec.ComputerID); err != nil {
				return err
			}
			if err = cleanup(r.Context(), tx, *rec.ComputerID, in); err != nil {
				return err
			}
		} else if rec.State == "approved" {
			if rec.ComputerID == nil {
				return fail(409, "conflict", "approved binding missing")
			}
			var state string
			if err = tx.QueryRow(r.Context(), `SELECT state FROM agent_pairing_computers WHERE id=$1`, *rec.ComputerID).Scan(&state); err != nil {
				return err
			}
			if state != "connected" {
				rec.State = "revoked"
			} else {
				rec.State = "redeemed"
			}
			if _, err = tx.Exec(r.Context(), `UPDATE agent_pairing_requests SET state=$2 WHERE id=$1`, rec.ID, rec.State); err != nil {
				return err
			}
		}
		v, err = view(r.Context(), tx, rec, !reconcile && rec.State == "redeemed")
		return err
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	if resultErr != nil {
		WriteError(w, resultErr)
		return
	}
	reply(w, v)
}
