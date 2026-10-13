// SPDX-License-Identifier: AGPL-3.0-only

// Package interaction implements the server's fenced control and request
// authority. I2 owns mounting these services; construction enables no route.
package interaction

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativeinteraction"
	"github.com/inspr-at/paimos/internal/questions"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const LeaseTTL = 30 * time.Second
const HeartbeatInterval = 5 * time.Second

// Authority checks the current immutable owner/actor/tool/settings grant, exact
// native turn and paired worker proof in this transaction. For view exports it
// also checks current owner consent and the specific view. No default exists.
// It must acquire no locks outside tenant -> tree -> rows, append no events,
// and perform no network or native I/O. Public IDs are never proof.
type Authority interface {
	AuthorizeTx(context.Context, pgx.Tx, tenant.Principal, nativecontract.Binding, string) error
	// This requirement comes from the current server-owned effect/tool policy.
	// A producer may request more human verification, never fewer checks.
	AuthorizeRequestTx(context.Context, pgx.Tx, tenant.Principal, nativecontract.Approval) (bool, error)
}

// StepUp consumes the existing human/phone proof for THIS exact decision,
// effect, request, turn and runtime under this transaction. A random public
// authorization ID is not sufficient. Nil keeps protected allows unavailable.
type StepUp interface {
	VerifyTx(context.Context, pgx.Tx, tenant.Principal, nativecontract.Approval, nativecontract.ApprovalDecision) error
}

type Options struct {
	Authority Authority
	StepUp    StepUp
	Clock     func(context.Context, pgx.Tx) (time.Time, error)
}

type Store struct {
	pool          *pgxpool.Pool
	authority     Authority
	stepUp        StepUp
	clock         func(context.Context, pgx.Tx) (time.Time, error)
	desk          *questions.Module
	validator     *nativecontract.Validator
	bootID        string
	capabilityKey [32]byte // process-local derivation only; never serialized
}

func New(pool *pgxpool.Pool, opts Options) (*Store, error) {
	if pool == nil || opts.Authority == nil {
		return nil, fault(503, nativecontract.ReasonQualificationMissing)
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	boot, err := uuid()
	if err != nil {
		return nil, err
	}
	s := &Store{pool: pool, authority: opts.Authority, stepUp: opts.StepUp, clock: opts.Clock, desk: questions.New(pool), validator: v, bootID: boot}
	if _, err = rand.Read(s.capabilityKey[:]); err != nil {
		return nil, err
	}
	return s, nil
}

func fault(status int, reason nativecontract.Reason) error {
	return nativeinteraction.Fail(status, reason)
}

func uuid() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

func digest(v any) []byte { raw, _ := json.Marshal(v); d := sha256.Sum256(raw); return d[:] }

func principal(ctx context.Context, a nativecontract.Actor) (tenant.Principal, error) {
	p, ok := tenant.PrincipalFrom(ctx)
	if !ok || p.ID != a.PrincipalID || p.TenantID != a.TenantID || string(p.Kind) != a.PrincipalKind || !slices.Equal(p.Scopes, a.KeyCeiling) {
		return p, fault(403, nativecontract.ReasonWrongPrincipal)
	}
	if p.Kind != tenant.Person && p.Kind != tenant.Agent {
		return p, fault(403, nativecontract.ReasonWrongPrincipal)
	}
	if p.Kind == tenant.Agent && p.KeyID == "" {
		return p, fault(403, nativecontract.ReasonGrantMissing)
	}
	return p, nil
}

func human(p tenant.Principal) error {
	if p.Kind != tenant.Person || p.KeyID != "" || p.AuthKeyID != "" || p.KeyCreatorID != "" {
		return fault(403, nativecontract.ReasonWrongPrincipal)
	}
	return nil
}

type locked struct {
	view                     nativecontract.SessionView
	policy                   nativecontract.Policy
	now                      time.Time
	paired, computer, ledger string
}

// transaction is the sole mutation entry. Access changes use the same fence;
// a slow preflight/desk write never substitutes for this final revalidation.
func (s *Store) transaction(ctx context.Context, a nativecontract.Actor, b nativecontract.Binding, action string, fn func(context.Context, pgx.Tx, tenant.Principal, *locked) error) error {
	if err := nativeinteraction.Validate(s.validator, "NativeBinding", b); err != nil {
		return err
	}
	p, err := principal(ctx, a)
	if err != nil {
		return err
	}
	if p.TenantID != b.TenantID {
		return fault(403, nativecontract.ReasonWrongInstance)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return db.InTenant(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "harness.read", authz.Scope{ProjectID: b.ProjectID}); err != nil {
			return fault(404, nativecontract.ReasonPermissionRevoked)
		}
		var raw, policy []byte
		l := &locked{}
		err := tx.QueryRow(ctx, `SELECT b.view,i.policy,b.paired_principal_id::text,b.computer_id::text,b.ledger_generation
 FROM native_session_bindings b JOIN native_session_intents i ON i.tenant_id=b.tenant_id AND i.owned_session_id=b.id
 JOIN nodes n ON n.tenant_id=b.tenant_id AND n.id=b.project_id
 JOIN harness_sessions h ON h.tenant_id=b.tenant_id AND h.id=b.current_harness_session_id
 WHERE b.id=$1 AND b.project_id=$2 AND n.deleted_at IS NULL AND h.stopped_at IS NULL AND h.archived_at IS NULL
 FOR NO KEY UPDATE OF b`, b.OwnedSessionID, b.ProjectID).Scan(&raw, &policy, &l.paired, &l.computer, &l.ledger)
		if errors.Is(err, pgx.ErrNoRows) {
			return fault(404, nativecontract.ReasonWrongGeneration)
		}
		if err != nil {
			return err
		}
		if len(raw) > 131072 || len(policy) > 16384 {
			return fault(413, nativecontract.ReasonBufferFull)
		}
		if err = json.Unmarshal(raw, &l.view); err != nil {
			return err
		}
		if err = json.Unmarshal(policy, &l.policy); err != nil {
			return err
		}
		if l.view.CurrentBinding == nil || !nativeinteraction.SameBinding(*l.view.CurrentBinding, b) {
			return fault(409, nativecontract.ReasonWrongGeneration)
		}
		if l.view.State != "running" && l.view.State != "idle" && l.view.State != "requires_action" && l.view.State != "taken" {
			return fault(409, nativecontract.ReasonOwnershipUnconfirmed)
		}
		var active bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1 AND kind=$2 AND status='active')`, p.ID, string(p.Kind)).Scan(&active); err != nil {
			return err
		}
		if !active {
			return fault(403, nativecontract.ReasonPermissionRevoked)
		}
		if err = s.authority.AuthorizeTx(ctx, tx, p, b, action); err != nil {
			return err
		}
		if err = s.refresh(ctx, tx, l); err != nil {
			return err
		}
		return fn(ctx, tx, p, l)
	})
}

func (s *Store) refresh(ctx context.Context, tx pgx.Tx, l *locked) error {
	var err error
	if s.clock == nil {
		err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&l.now)
	} else {
		l.now, err = s.clock(ctx, tx)
	}
	if err != nil {
		return err
	}
	if !l.now.Before(l.policy.ExpiresAt) {
		return fault(403, nativecontract.ReasonGrantExpired)
	}
	return nil
}

func require(ctx context.Context, tx pgx.Tx, p tenant.Principal, b nativecontract.Binding, permissions ...string) error {
	for _, permission := range permissions {
		if authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: b.ProjectID}) != nil {
			return fault(403, nativecontract.ReasonPermissionRevoked)
		}
	}
	return nil
}

func (s *Store) token(actor, viewer, epoch string) string {
	m := hmac.New(sha256.New, s.capabilityKey[:])
	m.Write([]byte(s.bootID + "\x00" + actor + "\x00" + viewer + "\x00" + epoch))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func tokenDigest(token string) []byte { d := sha256.Sum256([]byte(token)); return d[:] }

func (s *Store) event(ctx context.Context, tx pgx.Tx, p tenant.Principal, l *locked, request, kind, state string) error {
	b := l.view.CurrentBinding
	_, err := events.Append(ctx, tx, p, events.Change{NodeID: &b.ProjectID, Type: kind, After: map[string]any{
		"project_id": b.ProjectID, "owned_session_id": b.OwnedSessionID, "harness_session_id": b.HarnessSessionID,
		"runtime_epoch": b.RuntimeEpoch, "revision": l.view.Revision, "request_id": request, "state": state, "reason": nil, "operation_id": nil,
	}})
	return err // last: no further locks, including inside adapter callbacks
}

func (s *Store) saveView(ctx context.Context, tx pgx.Tx, l *locked) error {
	l.view.Revision++
	raw, err := json.Marshal(l.view)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE native_session_bindings SET view=$2 WHERE id=$1`, l.view.OwnedSessionID, raw)
	return err
}

func sameDigest(a, b []byte) bool { return bytes.Equal(a, b) }
