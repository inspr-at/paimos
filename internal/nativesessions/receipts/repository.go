// SPDX-License-Identifier: AGPL-3.0-only

// Package receipts implements the native store's durable receipt/final seams.
// Core route/supervisor wiring and authoritative native claims belong to I1/I2.
package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/chat"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Authority is supplied by the qualified native binding/claim store. Every
// method executes INSIDE the final transaction under tenant -> tree fences;
// it must perform no network/native I/O. Missing implementations fail closed.
// ReceiptTx validates the current paired principal, daemon/worker lease and
// exact authoritative intent, including consumed-input/native-turn evidence;
// exit_confirmed additionally requires observed owned root/group exit proof.
// FinalTx validates the current paired daemon and original, unexpired drain-only
// grant, actor/tool/settings grant, retention consent, account/conversation and
// CURRENT binding. Stopped/archived is allowed only by that exact drain grant;
// it never confers launch, input, resume or old-generation control authority.
type Authority interface {
	ReceiptTx(context.Context, pgx.Tx, nativecontract.DaemonActor, nativecontract.Receipt) (ReceiptAuthority, error)
	ReadTx(context.Context, pgx.Tx, nativecontract.Actor, nativecontract.Binding) error
	FinalTx(context.Context, pgx.Tx, nativecontract.DaemonActor, nativecontract.FinalPublish) (FinalAuthority, error)
}

type ReceiptAuthority struct {
	Intent        nativereceipts.Intent
	OwnerPersonID string
}

type FinalAuthority struct {
	Grant             nativecontract.FinalDrainGrant
	CurrentBinding    nativecontract.Binding
	Sender            tenant.Principal // immutable original actor and its current ceiling
	Target            chat.NativeFinalTarget
	ConversationRef   string
	IssuedAt          time.Time
	RetentionApproved bool
}

type Repository struct {
	pool      *pgxpool.Pool
	authority Authority
	chat      *chat.Module
	now       func() time.Time
	validator *nativecontract.Validator
}

func New(pool *pgxpool.Pool, authority Authority, chatModule *chat.Module, now func() time.Time) (*Repository, error) {
	if pool == nil || authority == nil {
		return nil, nativereceipts.Fail(nativecontract.ReasonQualificationMissing)
	}
	if now == nil {
		now = time.Now
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	return &Repository{pool: pool, authority: authority, chat: chatModule, now: now, validator: v}, nil
}

func principal(ctx context.Context, a nativecontract.Actor) (tenant.Principal, error) {
	p, ok := tenant.PrincipalFrom(ctx)
	if !ok || p.TenantID != a.TenantID || p.ID != a.PrincipalID || string(p.Kind) != a.PrincipalKind || !workorders.UUID(p.ID) || !workorders.UUID(p.TenantID) {
		return p, nativereceipts.Fail(nativecontract.ReasonWrongPrincipal)
	}
	return p, nil
}

func daemonPrincipal(ctx context.Context, a nativecontract.DaemonActor, b nativecontract.Binding) (tenant.Principal, error) {
	p, err := principal(ctx, a.Actor)
	if err != nil {
		return p, err
	}
	if p.Kind != tenant.Agent || p.TenantID != b.TenantID || a.DaemonID != b.DaemonID ||
		a.DaemonGeneration != b.DaemonGeneration || !workorders.UUID(a.ComputerID) || !workorders.UUID(a.LedgerGeneration) || !nativereceipts.ValidDigest(a.CapabilityDigest) {
		return p, nativereceipts.Fail(nativecontract.ReasonWrongGeneration)
	}
	return p, nil
}

// A restarted daemon may drain an exact older runtime only after Authority
// revalidates its CURRENT pairing and the original drain-only grant. Reports
// and control continue using daemonPrincipal's strict active generation check.
func finalPrincipal(ctx context.Context, a nativecontract.DaemonActor, b nativecontract.Binding) (tenant.Principal, error) {
	p, err := principal(ctx, a.Actor)
	if err != nil {
		return p, err
	}
	if p.Kind != tenant.Agent || p.TenantID != b.TenantID || len(a.DaemonID) < 1 || len(a.DaemonID) > 128 || len(a.DaemonGeneration) < 1 || len(a.DaemonGeneration) > 128 ||
		!workorders.UUID(a.ComputerID) || !workorders.UUID(a.LedgerGeneration) || !nativereceipts.ValidDigest(a.CapabilityDigest) {
		return p, nativereceipts.Fail(nativecontract.ReasonWrongPrincipal)
	}
	return p, nil
}

// write always takes the canonical access fences before authority/record work.
// C0's three-second admission budget bounds the transaction independently of
// the native process lifetime. No transport call is made inside this write.
func (s *Repository) write(ctx context.Context, p tenant.Principal, fn func(context.Context, pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return db.InTenant(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTree(ctx, tx, p.TenantID); err != nil {
			return err
		}
		// InTenant's initial visibility can predate a wait on the access fence.
		// Refresh it under that fence so revoked project access cannot survive
		// in an RLS setting even when a separate role still grants harness.read.
		var creator any
		if p.KeyCreatorID != "" {
			creator = p.KeyCreatorID
		}
		if _, err := tx.Exec(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid)`, p.TenantID, p.ID, creator); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

// Report persists metadata only and never calls a native transport. An exact
// retry updates a bounded counter, without duplicating an event or advancing
// evidence. Callers replay journaled states in order after an API outage.
func (s *Repository) Report(ctx context.Context, daemon nativecontract.DaemonActor, in nativecontract.Receipt) (nativecontract.OperationResult, error) {
	out := nativecontract.OperationResult{}
	if in.Binding == nil {
		return out, nativereceipts.Fail(nativecontract.ReasonIdentityMismatch)
	}
	if err := nativereceipts.Validate(s.validator, "NativeReceipt", in); err != nil {
		return out, err
	}
	p, err := daemonPrincipal(ctx, daemon, *in.Binding)
	if err != nil {
		return out, err
	}
	err = s.write(ctx, p, func(ctx context.Context, tx pgx.Tx) error {
		a, err := s.authority.ReceiptTx(ctx, tx, daemon, in)
		if err != nil {
			return err
		}
		intent := a.Intent
		if intent.Receipt.Binding == nil || !intent.Receipt.Binding.Equal(*in.Binding) || intent.Receipt.OperationID != in.OperationID ||
			intent.Receipt.RequestID != in.RequestID || intent.Receipt.Operation != in.Operation || !sameID(intent.Receipt.InputID, in.InputID) ||
			!workorders.UUID(a.OwnerPersonID) || !workorders.UUID(intent.ActorID) || !nativereceipts.ValidDigest(intent.PayloadSHA256) {
			return nativereceipts.Fail(nativecontract.ReasonIdentityMismatch)
		}
		if err := authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: in.Binding.ProjectID}); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, tenant.Principal{TenantID: p.TenantID, ID: a.OwnerPersonID, Kind: tenant.Person}, "harness.read", authz.Scope{ProjectID: in.Binding.ProjectID}); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, tenant.Principal{TenantID: p.TenantID, ID: a.OwnerPersonID, Kind: tenant.Person}, "nodes.read", authz.Scope{ProjectID: in.Binding.ProjectID}); err != nil {
			return err
		}
		var raw []byte
		var actor, owner, digest string
		err = tx.QueryRow(ctx, `SELECT receipt,actor_principal_id::text,owner_person_id::text,payload_sha256 FROM native_session_receipts WHERE tenant_id=$1 AND operation_id=$2 FOR NO KEY UPDATE`, p.TenantID, in.OperationID).Scan(&raw, &actor, &owner, &digest)
		created := errors.Is(err, pgx.ErrNoRows)
		if err != nil && !created {
			return err
		}
		if created {
			if in.State != "accepted" || in.SourceSequence != "0" || in.Reason != nil {
				return nativereceipts.Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
			}
		} else {
			if actor != intent.ActorID || owner != a.OwnerPersonID || digest != intent.PayloadSHA256 {
				return nativereceipts.Fail(nativecontract.ReasonIdentityMismatch)
			}
			var before nativecontract.Receipt
			if err = s.validator.DecodeJSON("NativeReceipt", raw, &before); err != nil {
				return err
			}
			if err = nativereceipts.Transition(before, in); err != nil {
				return err
			}
			if before.State == in.State {
				_, err = tx.Exec(ctx, `UPDATE native_session_receipts SET attempts=least(attempts+1,65535) WHERE tenant_id=$1 AND operation_id=$2`, p.TenantID, in.OperationID)
				out.Receipt = before
				return err
			}
		}
		in.UpdatedAt = s.now().UTC()
		raw, err = json.Marshal(in)
		if err != nil {
			return err
		}
		if created {
			_, err = tx.Exec(ctx, `INSERT INTO native_session_receipts(tenant_id,operation_id,request_id,project_id,owned_session_id,owner_person_id,actor_principal_id,payload_sha256,expires_at,receipt,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`, p.TenantID, in.OperationID, in.RequestID, in.Binding.ProjectID, in.Binding.OwnedSessionID, a.OwnerPersonID, intent.ActorID, intent.PayloadSHA256, intent.ExpiresAt, raw, in.UpdatedAt)
		} else {
			_, err = tx.Exec(ctx, `UPDATE native_session_receipts SET receipt=$3,updated_at=$4 WHERE tenant_id=$1 AND operation_id=$2`, p.TenantID, in.OperationID, raw, in.UpdatedAt)
		}
		if err != nil {
			return err
		}
		// Audit payload is the C0 content-free shape. Event counter is LAST.
		b := in.Binding
		payload := nativecontract.AuditEvent{ProjectID: b.ProjectID, OwnedSessionID: b.OwnedSessionID, HarnessSessionID: &b.HarnessSessionID, RuntimeEpoch: &b.RuntimeEpoch, Revision: b.BindingRevision, RequestID: in.RequestID, State: in.State, Reason: in.Reason, OperationID: &in.OperationID}
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &b.ProjectID, Type: "native_session.receipt_changed", After: payload})
		out.Receipt = in
		return err
	})
	if err != nil {
		return nativecontract.OperationResult{}, err
	}
	return out, nil
}

func sameID(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
