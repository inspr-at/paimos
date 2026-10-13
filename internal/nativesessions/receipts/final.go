// SPDX-License-Identifier: AGPL-3.0-only

package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/chat"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Sink captures immutable authenticated daemon identity, not wire-supplied
// authority. The transport must reauthenticate on every request; an API restart
// cannot renew the publication window or turn this into a command/launch claim.
type Sink struct {
	Repository *Repository
	Daemon     nativecontract.DaemonActor
}

func (s Sink) Persist(ctx context.Context, in nativecontract.FinalDelivery) (nativecontract.FinalReceipt, error) {
	if s.Repository == nil {
		return nativecontract.FinalReceipt{}, nativereceipts.Fail(nativecontract.ReasonFeatureDisabled)
	}
	return s.Repository.Persist(ctx, s.Daemon, in)
}

var _ nativecontract.FinalSink = Sink{}

func (s *Repository) Persist(ctx context.Context, daemon nativecontract.DaemonActor, in nativecontract.FinalPublish) (nativecontract.NativeFinalReceipt, error) {
	out := nativecontract.NativeFinalReceipt{}
	if len(in.Text) > nativereceipts.MaxTextBytes || !utf8.ValidString(in.Text) {
		return out, nativereceipts.Fail(nativecontract.ReasonBufferFull)
	}
	if err := nativereceipts.Validate(s.validator, "NativeFinalPublish", in); err != nil {
		return out, err
	}
	p, err := finalPrincipal(ctx, daemon, in.ExpectedBinding)
	if err != nil {
		return out, err
	}
	digest, err := nativereceipts.FinalDigest(in)
	if err != nil {
		return out, err
	}
	var committed chat.NativeFinalWrite
	err = s.write(ctx, p, func(ctx context.Context, tx pgx.Tx) error {
		// Even an exact final retry revalidates current authority; process exit,
		// an old generation or a persisted ID is never a permission bypass.
		a, err := s.authority.FinalTx(ctx, tx, daemon, in)
		if err != nil {
			return err
		}
		if err = nativereceipts.Validate(s.validator, "NativeFinalDrainGrant", a.Grant); err != nil {
			return err
		}
		if err = nativereceipts.Validate(s.validator, "NativeBinding", a.CurrentBinding); err != nil {
			return err
		}
		b := in.ExpectedBinding
		if !a.Grant.Binding.Equal(b) || a.Grant.GrantID != in.DrainGrantID || a.CurrentBinding.TenantID != b.TenantID ||
			a.CurrentBinding.ProjectID != b.ProjectID || a.CurrentBinding.OwnedSessionID != b.OwnedSessionID ||
			!sameID(a.CurrentBinding.RunID, b.RunID) || a.Grant.ConversationRef != a.ConversationRef ||
			a.Sender.TenantID != p.TenantID || a.Target.SessionID != b.HarnessSessionID {
			return nativereceipts.Fail(nativecontract.ReasonWrongGeneration)
		}
		if !s.now().Before(a.Grant.ExpiresAt) {
			return nativereceipts.Fail(nativecontract.ReasonPublicationGrantExpired)
		}
		if !a.RetentionApproved || a.IssuedAt.IsZero() || a.Grant.ExpiresAt.After(a.IssuedAt.Add(nativereceipts.MaxRetention)) {
			return nativereceipts.Fail(nativecontract.ReasonRetentionUnapproved)
		}
		a.Target.ExpiresAt, a.Target.Now = a.Grant.ExpiresAt, s.now
		if err = authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: b.ProjectID}); err != nil {
			return err
		}
		var raw []byte
		// Completion has to be durably correlated to this exact runtime/input.
		if err = tx.QueryRow(ctx, `SELECT receipt FROM native_session_receipts WHERE tenant_id=$1 AND owned_session_id=$2 AND receipt->>'input_id'=$3 AND receipt->>'state'='turn_completed' ORDER BY operation_id LIMIT 1 FOR NO KEY UPDATE`, p.TenantID, b.OwnedSessionID, in.InputID).Scan(&raw); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nativereceipts.Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
			}
			return err
		}
		var completed nativecontract.Receipt
		if err = s.validator.DecodeJSON("NativeReceipt", raw, &completed); err != nil {
			return err
		}
		if completed.Operation != "input" || completed.Binding == nil || !completed.Binding.Equal(b) || completed.TurnRef == nil || *completed.TurnRef != in.TurnRef || completed.SourceSequence == "0" {
			return nativereceipts.Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
		}
		var priorDigest, priorSender, priorOwner, priorThread string
		var messageID *string
		var state string
		err = tx.QueryRow(ctx, `SELECT publication_sha256,sender_principal_id::text,owner_person_id::text,conversation_id::text,message_id::text,state,updated_at FROM native_session_finals WHERE tenant_id=$1 AND final_id=$2 FOR NO KEY UPDATE`, p.TenantID, in.FinalID).Scan(&priorDigest, &priorSender, &priorOwner, &priorThread, &messageID, &state, &out.UpdatedAt)
		if err == nil {
			if digest != priorDigest || priorSender != a.Sender.ID || priorOwner != a.Target.OwnerPersonID || priorThread != a.Target.ConversationID {
				return nativereceipts.Fail(nativecontract.ReasonIdentityMismatch)
			}
			// Re-run the narrow helper even on retry to check current target
			// permissions and chat privacy. storeFinal itself is idempotent.
			committed, err = s.chat.StoreNativeFinalTx(ctx, tx, a.Sender, a.Target, in)
			if err != nil {
				return err
			}
			if state != "persisted" || messageID == nil || committed.Receipt.MessageID == nil || *messageID != *committed.Receipt.MessageID {
				return nativereceipts.Fail(nativecontract.ReasonIdentityMismatch)
			}
			out.FinalID, out.MessageID, out.State = in.FinalID, messageID, state
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var count, bytes int64
		if err = tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(text_bytes),0) FROM native_session_finals WHERE tenant_id=$1 AND drain_grant_id=$2`, p.TenantID, in.DrainGrantID).Scan(&count, &bytes); err != nil {
			return err
		}
		if count >= a.Grant.MaximumFinals || bytes+int64(len(in.Text)) > a.Grant.MaximumBytes {
			return nativereceipts.Fail(nativecontract.ReasonBufferFull)
		}
		bindingJSON, err := json.Marshal(b)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if _, err = tx.Exec(ctx, `INSERT INTO native_session_finals(tenant_id,final_id,project_id,owned_session_id,owner_person_id,sender_principal_id,conversation_id,drain_grant_id,binding,input_id,turn_ref,publication_sha256,text_bytes,state,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'pending',$14,$14)`, p.TenantID, in.FinalID, b.ProjectID, b.OwnedSessionID, a.Target.OwnerPersonID, a.Sender.ID, a.Target.ConversationID, in.DrainGrantID, bindingJSON, in.InputID, in.TurnRef, digest, len(in.Text), now); err != nil {
			return err
		}
		committed, err = s.chat.StoreNativeFinalTx(ctx, tx, a.Sender, a.Target, in)
		if err != nil {
			return err
		}
		out = committed.Receipt
		out.UpdatedAt = now
		// The projection row was already acquired before storeFinal's event
		// counter. Updating this held row acquires no later row/blob lock.
		_, err = tx.Exec(ctx, `UPDATE native_session_finals SET message_id=$3,state='persisted' WHERE tenant_id=$1 AND final_id=$2`, p.TenantID, in.FinalID, out.MessageID)
		return err
	})
	if err == nil {
		s.chat.NativeFinalCommitted(committed)
		return out, nil
	}
	// Explicit refusals are terminal, visible outcomes. Transient storage/API
	// errors retain the local final, rather than pretending it was persisted.
	reason, state := nativecontract.Reason(""), "rejected"
	var refused *nativereceipts.Error
	var target *workorders.Error
	var deadline *nativecontract.ValidationError
	if errors.Is(err, authz.ErrForbidden) || errors.As(err, &target) && (target.Status == 403 || target.Status == 404) {
		reason = nativecontract.ReasonPermissionRevoked
	}
	if errors.As(err, &refused) {
		switch refused.Reason {
		case nativecontract.ReasonPublicationGrantExpired, nativecontract.ReasonGrantExpired, nativecontract.ReasonFinalDeliveryExpired:
			reason, state = refused.Reason, "expired"
		case nativecontract.ReasonRetentionUnapproved, nativecontract.ReasonPermissionRevoked, nativecontract.ReasonWrongGeneration, nativecontract.ReasonGrantMissing:
			reason = refused.Reason
		}
	}
	if errors.As(err, &deadline) && deadline.Reason == nativecontract.ReasonPublicationGrantExpired {
		reason, state = deadline.Reason, "expired"
	}
	if reason != "" {
		return nativecontract.NativeFinalReceipt{FinalID: in.FinalID, State: state, Reason: &reason, UpdatedAt: s.now().UTC()}, nil
	}
	return nativecontract.NativeFinalReceipt{}, err
}
