// SPDX-License-Identifier: AGPL-3.0-only
package stepup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/phoneapprovals"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const RequestLifetime = 15 * time.Minute
const ProofLifetime = 2 * time.Minute

type ApprovalRequest struct {
	ID            string          `json:"id"`
	RequestedBy   string          `json:"requested_by"`
	SessionID     *string         `json:"session_id,omitempty"`
	ProjectID     *string         `json:"project_id,omitempty"`
	Permission    string          `json:"permission"`
	Payload       json.RawMessage `json:"payload"`
	Before        json.RawMessage `json:"before"`
	After         json.RawMessage `json:"after"`
	BeforeHash    string          `json:"before_hash"`
	AfterHash     string          `json:"after_hash"`
	Digest        string          `json:"request_digest"`
	CreatedAt     time.Time       `json:"created_at"`
	ExpiresAt     time.Time       `json:"expires_at"`
	State         string          `json:"state"`
	Revision      int64           `json:"revision"`
	DecidedBy     *string         `json:"decided_by,omitempty"`
	DecidedByName *string         `json:"decided_by_name,omitempty"`
	Decision      *string         `json:"decision,omitempty"`
	Method        *string         `json:"method,omitempty"`
	AuthTime      *time.Time      `json:"auth_time,omitempty"`
	AppliedAt     *time.Time      `json:"applied_at,omitempty"`
	DecidedAt     *time.Time      `json:"decided_at,omitempty"`
}
type Create struct {
	Payload    json.RawMessage `json:"payload"`
	BeforeHash string          `json:"before_hash"`
	SessionID  string          `json:"session_id,omitempty"`
}
type Decide struct {
	Digest   string `json:"request_digest"`
	Revision int64  `json:"revision"`
}
type Approve struct {
	Decide
	phoneapprovals.Proof
}

type Module struct {
	pool    *pgxpool.Pool
	phone   *phoneapprovals.Module
	origin  string
	targets map[string]Target
	now     func() time.Time
	// Reauthenticate starts the existing OIDC flow, with its existing signing
	// key and callback. No credential or proof is accepted from an agent.
	Reauthenticate func(http.ResponseWriter, *http.Request, tenant.Principal, ReauthStart) (string, error)
}
type ReauthStart struct {
	RequestID, ChallengeID, Digest string
	StartedAt                      time.Time
}

func New(pool *pgxpool.Pool, phone *phoneapprovals.Module, origin string) *Module {
	return &Module{pool: pool, phone: phone, origin: origin, targets: map[string]Target{"feature": FeatureTarget{}}, now: time.Now}
}
func (m *Module) target(raw json.RawMessage) (Target, json.RawMessage, string, error) {
	if len(raw) > payloadLimit {
		return nil, nil, "", fault(400, "payload exceeds 32 KiB")
	}
	var tag struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(raw, &tag) != nil {
		return nil, nil, "", fault(400, "invalid payload")
	}
	target := m.targets[tag.Kind]
	if target == nil {
		return nil, nil, "", fault(400, "unsupported protected change")
	}
	canonical, project, err := target.Parse(raw)
	return target, canonical, project, err
}
func (m *Module) transaction(ctx context.Context, p tenant.Principal, fn func(pgx.Tx) error) error {
	ctx = tenant.WithPrincipal(ctx, p)
	// Native request visibility is explicitly filtered by each target's authority.
	ctx = db.AllProjects(ctx, "step-up: request ownership and target permission checked inside the transaction")
	return db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTree(ctx, tx, p.TenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}
func allowed(ctx context.Context, tx pgx.Tx, p tenant.Principal, r ApprovalRequest) error {
	project := ""
	if r.ProjectID != nil {
		project = *r.ProjectID
	}
	if p.Kind == tenant.Agent {
		if r.RequestedBy != p.ID {
			return fault(404, "request unavailable")
		}
		return authz.RequireTx(ctx, tx, p, "approvals.request", authz.Scope{ProjectID: project})
	}
	if p.Kind != tenant.Person || p.KeyCreatorID != "" {
		return authz.ErrForbidden
	}
	return authz.RequireTx(ctx, tx, p, r.Permission, authz.Scope{ProjectID: project})
}

const requestColumns = `id::text,requested_by::text,session_id::text,project_id::text,permission,payload,before_value,after_value,before_hash,after_hash,request_digest,created_at,expires_at,state,revision,decided_by::text,decision,method,auth_time,applied_at,decided_at`

func read(ctx context.Context, tx pgx.Tx, id string, lock bool) (ApprovalRequest, error) {
	var r ApprovalRequest
	sql := `SELECT ` + requestColumns + ` FROM stepup_requests WHERE id=$1`
	if lock {
		sql += ` FOR NO KEY UPDATE`
	}
	err := scan(tx.QueryRow(ctx, sql, id), &r)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, fault(404, "request unavailable")
	}
	return r, err
}
func scan(row interface{ Scan(...any) error }, r *ApprovalRequest) error {
	return row.Scan(&r.ID, &r.RequestedBy, &r.SessionID, &r.ProjectID, &r.Permission, &r.Payload, &r.Before, &r.After, &r.BeforeHash, &r.AfterHash, &r.Digest, &r.CreatedAt, &r.ExpiresAt, &r.State, &r.Revision, &r.DecidedBy, &r.Decision, &r.Method, &r.AuthTime, &r.AppliedAt, &r.DecidedAt)
}
func decorate(ctx context.Context, tx pgx.Tx, r *ApprovalRequest) error {
	if r.DecidedBy == nil {
		return nil
	}
	return tx.QueryRow(ctx, `SELECT left(name,256) FROM principals WHERE id=$1`, *r.DecidedBy).Scan(&r.DecidedByName)
}
func requestDigest(p tenant.Principal, r ApprovalRequest) string {
	raw, _ := json.Marshal([]any{"aeon.stepup.request.v1", p.TenantID, r.ID, r.RequestedBy, r.SessionID, r.ProjectID, r.Permission, r.Payload, r.BeforeHash, r.AfterHash, r.CreatedAt.UTC(), r.ExpiresAt.UTC()})
	return Hash(raw)
}
func (m *Module) Create(ctx context.Context, p tenant.Principal, in Create) (ApprovalRequest, error) {
	var out ApprovalRequest
	if p.Kind != tenant.Agent {
		return out, authz.ErrForbidden
	}
	if !validHash(in.BeforeHash) || in.SessionID != "" && !ValidID(in.SessionID) {
		return out, fault(400, "invalid request")
	}
	target, payload, project, err := m.target(in.Payload)
	if err != nil {
		return out, err
	}
	err = m.transaction(ctx, p, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "approvals.request", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		if in.SessionID != "" {
			var owned bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions WHERE tenant_id=$1 AND id=$2 AND agent_principal_id=$3 AND project_id IS NOT DISTINCT FROM $4::uuid)`, p.TenantID, in.SessionID, p.ID, nullable(project)).Scan(&owned); err != nil {
				return err
			}
			if !owned {
				return fault(403, "session does not belong to this request")
			}
		}
		before, after, err := target.Snapshot(ctx, tx, p, payload)
		if err != nil {
			return err
		}
		var change featureChange
		var snapshot featureSnapshot
		// Every adapter binds its own concurrency token. The initial adapter's
		// expected revision must agree with the server snapshot as well as the hash.
		if _, ok := target.(FeatureTarget); ok {
			if err := json.Unmarshal(payload, &change); err != nil {
				return err
			}
			if err := json.Unmarshal(before, &snapshot); err != nil {
				return err
			}
			if snapshot.Revision != *change.Revision {
				return fault(409, "target changed; read it again")
			}
		}
		if Hash(before) != in.BeforeHash {
			return fault(409, "before hash mismatch; read the target again")
		}
		now := m.now().UTC().Truncate(time.Microsecond)
		out = ApprovalRequest{RequestedBy: p.ID, SessionID: nullable(in.SessionID), ProjectID: nullable(project), Permission: target.Permission(), Payload: payload, Before: before, After: after, BeforeHash: Hash(before), AfterHash: Hash(after), CreatedAt: now, ExpiresAt: now.Add(RequestLifetime), State: "pending", Revision: 1}
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&out.ID); err != nil {
			return err
		}
		out.Digest = requestDigest(p, out)
		_, err = tx.Exec(ctx, `INSERT INTO stepup_requests(tenant_id,id,requested_by,session_id,project_id,permission,payload,before_value,after_value,before_hash,after_hash,request_digest,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, p.TenantID, out.ID, p.ID, out.SessionID, out.ProjectID, out.Permission, out.Payload, out.Before, out.After, out.BeforeHash, out.AfterHash, out.Digest, out.CreatedAt, out.ExpiresAt)
		if err != nil {
			return err
		}
		return audit(ctx, tx, p, out)
	})
	return out, err
}
func audit(ctx context.Context, tx pgx.Tx, p tenant.Principal, r ApprovalRequest) error {
	// Native typed snapshots contain no credentials. Proofs/tokens never reach
	// this event; method and auth_time describe only verified authentication.
	_, err := events.Append(ctx, tx, p, events.Change{Type: "stepup." + r.State, NodeID: r.ProjectID, Before: r.Before, After: map[string]any{"request_id": r.ID, "requested_by": r.RequestedBy, "approved_by": actorFor(r, "approve"), "declined_by": actorFor(r, "decline"), "decided_by": r.DecidedBy, "method": r.Method, "auth_time": r.AuthTime, "before": r.Before, "after": r.After, "request_digest": r.Digest, "expires_at": r.ExpiresAt, "outcome": r.State}})
	return err
}
func actorFor(r ApprovalRequest, decision string) *string {
	if r.Decision != nil && *r.Decision == decision {
		return r.DecidedBy
	}
	return nil
}
func (m *Module) settle(ctx context.Context, tx pgx.Tx, p tenant.Principal, r *ApprovalRequest, state, decision, method string, authTime *time.Time) error {
	now := m.now().UTC()
	if state == "expired" {
		now = r.ExpiresAt
	}
	r.State = state
	r.Decision = &decision
	r.DecidedBy = &p.ID
	r.Method = nil
	if method != "" {
		r.Method = &method
	}
	r.AuthTime = authTime
	r.DecidedAt = &now
	r.Revision++
	if state == "applied" {
		r.AppliedAt = &now
	}
	tag, err := tx.Exec(ctx, `UPDATE stepup_requests SET state=$2,revision=revision+1,decided_by=$3,decision=$4,method=$5,auth_time=$6,applied_at=$7,decided_at=$8 WHERE id=$1 AND state='pending'`, r.ID, state, p.ID, decision, r.Method, authTime, r.AppliedAt, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fault(409, "request already decided")
	}
	if err := decorate(ctx, tx, r); err != nil {
		return err
	}
	return audit(ctx, tx, p, *r)
}
func (m *Module) expire(ctx context.Context, tx pgx.Tx, p tenant.Principal, r *ApprovalRequest) error {
	if r.State == "pending" && !r.ExpiresAt.After(m.now()) {
		return m.settle(ctx, tx, p, r, "expired", "expire", "", nil)
	}
	return nil
}
func (m *Module) Get(ctx context.Context, p tenant.Principal, id string) (ApprovalRequest, error) {
	var out ApprovalRequest
	if !ValidID(id) {
		return out, fault(404, "request unavailable")
	}
	err := m.transaction(ctx, p, func(tx pgx.Tx) error {
		var err error
		out, err = read(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err = allowed(ctx, tx, p, out); err != nil {
			return err
		}
		if err = m.expire(ctx, tx, p, &out); err != nil {
			return err
		}
		return decorate(ctx, tx, &out)
	})
	return out, err
}
func bound(r ApprovalRequest, in Decide) error {
	if r.Digest != in.Digest || r.Revision != in.Revision {
		return fault(409, "request changed; review it again")
	}
	return nil
}
func (m *Module) Decide(ctx context.Context, p tenant.Principal, id string, in Decide, decision string, proof *phoneapprovals.Proof) (ApprovalRequest, error) {
	return m.decide(ctx, p, id, in, decision, func(tx pgx.Tx, r ApprovalRequest) (string, time.Time, error) {
		if m.phone == nil || proof == nil {
			return "", time.Time{}, fault(403, "fresh verification required")
		}
		return m.phone.VerifyStepUpTx(ctx, tx, p, id, r.Digest, *proof)
	})
}
func (m *Module) decide(ctx context.Context, p tenant.Principal, id string, in Decide, decision string, verify func(pgx.Tx, ApprovalRequest) (string, time.Time, error)) (ApprovalRequest, error) {
	var out ApprovalRequest
	if !ValidID(id) {
		return out, fault(404, "request unavailable")
	}
	if decision == "withdraw" {
		if p.Kind != tenant.Agent {
			return out, authz.ErrForbidden
		}
	} else if p.Kind != tenant.Person || p.KeyCreatorID != "" {
		return out, authz.ErrForbidden
	}
	err := m.transaction(ctx, p, func(tx pgx.Tx) error {
		var err error
		out, err = read(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err = allowed(ctx, tx, p, out); err != nil {
			return err
		}
		if err = m.expire(ctx, tx, p, &out); err != nil {
			return err
		}
		if out.State != "pending" {
			return decorate(ctx, tx, &out)
		}
		if err = bound(out, in); err != nil {
			return err
		}
		switch decision {
		case "withdraw":
			return m.settle(ctx, tx, p, &out, "withdrawn", decision, "", nil)
		case "decline":
			return m.settle(ctx, tx, p, &out, "declined", decision, "", nil)
		case "approve":
			// Target locks precede proof/passkey row locks; all precede audit.
			target, payload, project, err := m.target(out.Payload)
			if err != nil {
				return err
			}
			if target.Permission() != out.Permission || nullableValue(out.ProjectID) != project || requestDigest(p, out) != out.Digest {
				return fault(409, "request integrity mismatch")
			}
			before, after, err := target.Snapshot(ctx, tx, p, payload)
			if err != nil {
				var f *problem
				if !errors.As(err, &f) || f.status != 404 {
					return err
				}
				before, after = nil, nil
			}
			method, authTime, err := verify(tx, out)
			if err != nil {
				return err
			}
			if !out.ExpiresAt.After(m.now()) {
				return m.settle(ctx, tx, p, &out, "expired", "expire", "", nil)
			}
			if Hash(before) != out.BeforeHash || Hash(after) != out.AfterHash {
				return m.settle(ctx, tx, p, &out, "stale", decision, method, &authTime)
			}
			// An apply error rolls back only the attempted mutation. The settled
			// failed outcome remains honest and cannot be retried with the same proof.
			savepoint, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			if err = target.Apply(ctx, savepoint, p, payload, out.After); err != nil {
				if rollbackErr := savepoint.Rollback(ctx); rollbackErr != nil {
					return rollbackErr
				}
				return m.settle(ctx, tx, p, &out, "failed", decision, method, &authTime)
			}
			if err = savepoint.Commit(ctx); err != nil {
				return err
			}
			return m.settle(ctx, tx, p, &out, "applied", decision, method, &authTime)
		default:
			return fault(400, "invalid decision")
		}
	})
	return out, err
}
func nullableValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
