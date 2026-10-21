// SPDX-License-Identifier: AGPL-3.0-only

package receipts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/nativesessions/consent"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// RuntimeProof is request-scoped private evidence from the owning daemon. The
// transport must bind it to its authenticated worker lease, never an API body
// or a public session selector. ReceiptTx independently checks the lease against
// the exact persisted generation and the evidence against the original command.
type RuntimeProof struct {
	WorkerLease string
	Identity    nativecontract.NativeIdentity
	Evidence    nativecontract.NativeEvidence
	Exit        *nativecontract.ExitEvidence
}

func (RuntimeProof) MarshalJSON() ([]byte, error) {
	return nil, errors.New("native runtime proof is private")
}
func (*RuntimeProof) UnmarshalJSON([]byte) error {
	return errors.New("native runtime proof is private")
}

type runtimeProofKey struct{}

func WithRuntimeProof(ctx context.Context, proof RuntimeProof) context.Context {
	return context.WithValue(ctx, runtimeProofKey{}, proof)
}

// CurrentAuthority is the production transaction authority. No projection,
// cached policy, receipt ID or caller-supplied retention boolean is authority.
type CurrentAuthority struct{ Consent consent.Authority }

var _ Authority = CurrentAuthority{}

func currentFailure(reason nativecontract.Reason) error { return nativereceipts.Fail(reason) }

func currentError(err error) error {
	var refusal *nativecontract.ValidationError
	if errors.As(err, &refusal) {
		return currentFailure(refusal.Reason)
	}
	return err
}

func (a CurrentAuthority) pairedTx(ctx context.Context, tx pgx.Tx, daemon nativecontract.DaemonActor, b nativecontract.Binding, session consent.Session, draining bool) (tenant.Principal, RuntimeProof, error) {
	p, ok := tenant.PrincipalFrom(ctx)
	proof, hasProof := ctx.Value(runtimeProofKey{}).(RuntimeProof)
	if !ok || !hasProof || p.Kind != tenant.Agent || p.ID != daemon.PrincipalID || p.TenantID != b.TenantID || p.KeyID == "" ||
		daemon.ComputerID != session.Intent.ComputerID || len(proof.WorkerLease) < 16 || len(proof.WorkerLease) > 256 {
		return p, proof, currentFailure(nativecontract.ReasonPermissionRevoked)
	}
	var lease []byte
	var active bool
	err := tx.QueryRow(ctx, `SELECT h.lease_digest,h.stopped_at IS NULL AND h.archived_at IS NULL
 FROM native_session_bindings b
 JOIN agent_pairing_computers c ON c.tenant_id=b.tenant_id AND c.id=b.computer_id
 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 JOIN agent_pairing_enrollments e ON e.tenant_id=c.tenant_id AND e.computer_id=c.id AND e.account_id=b.account_id
 JOIN agent_accounts account ON account.tenant_id=e.tenant_id AND account.id=e.account_id
 JOIN harness_sessions h ON h.tenant_id=b.tenant_id AND h.id=$4 AND h.agent_principal_id=c.principal_id
 WHERE b.tenant_id=$1 AND b.id=$2 AND b.paired_principal_id=$3 AND c.principal_id=$3 AND c.key_id=$5
 AND c.id=$6 AND c.daemon_id=$7 AND c.state='connected' AND q.state='redeemed'
 AND e.state='connected' AND e.ongoing_approved_at IS NOT NULL AND account.archived_at IS NULL
 AND account.last_daemon_generation=$8 AND c.ledger_generation=$9
 FOR NO KEY UPDATE OF c,h`, b.TenantID, b.OwnedSessionID, p.ID, b.HarnessSessionID, p.KeyID, daemon.ComputerID, daemon.DaemonID, daemon.DaemonGeneration, daemon.LedgerGeneration).Scan(&lease, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, proof, currentFailure(nativecontract.ReasonPermissionRevoked)
	}
	if err != nil {
		return p, proof, err
	}
	digest := sha256.Sum256([]byte(proof.WorkerLease))
	if subtle.ConstantTimeCompare(lease, digest[:]) != 1 || !draining && !active ||
		daemon.CapabilityDigest != session.View.Capabilities.QualificationDigest ||
		!draining && (daemon.DaemonID != b.DaemonID || daemon.DaemonGeneration != b.DaemonGeneration) {
		return p, proof, currentFailure(nativecontract.ReasonPermissionRevoked)
	}
	if authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: b.ProjectID}) != nil {
		return p, proof, currentFailure(nativecontract.ReasonPermissionRevoked)
	}
	return p, proof, nil
}

func (a CurrentAuthority) ReceiptTx(ctx context.Context, tx pgx.Tx, daemon nativecontract.DaemonActor, in nativecontract.Receipt) (ReceiptAuthority, error) {
	var out ReceiptAuthority
	if in.Binding == nil {
		return out, currentFailure(nativecontract.ReasonIdentityMismatch)
	}
	b := *in.Binding
	s, err := a.Consent.SessionTx(ctx, tx, b)
	if err != nil {
		return out, currentError(err)
	}
	_, proof, err := a.pairedTx(ctx, tx, daemon, b, s, in.State == "exit_confirmed")
	if err != nil {
		return out, err
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT intent FROM native_session_operations WHERE tenant_id=$1 AND operation_id=$2 AND owned_session_id=$3 AND project_id=$4 FOR SHARE`, b.TenantID, in.OperationID, b.OwnedSessionID, b.ProjectID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, currentFailure(nativecontract.ReasonPermissionRevoked)
	}
	if err != nil {
		return out, err
	}
	if len(raw) > 16384 || json.Unmarshal(raw, &out.Intent) != nil {
		return out, currentFailure(nativecontract.ReasonIdentityMismatch)
	}
	r := out.Intent.Receipt
	if r.Binding == nil || !r.Binding.Equal(b) || r.RequestID != in.RequestID || r.OperationID != in.OperationID || r.Operation != in.Operation ||
		!sameID(r.InputID, in.InputID) || out.Intent.ActorID != s.Original.ID {
		return out, currentFailure(nativecontract.ReasonIdentityMismatch)
	}
	// Accepted/queued are never delivery. Native outcomes require a provider
	// observation with the exact input, native turn, sequence and runtime.
	if in.State == "write_acknowledged" || in.State == "turn_started" || in.State == "turn_completed" || in.State == "applied" {
		e := proof.Evidence
		sequence, parseErr := strconv.ParseUint(in.SourceSequence, 10, 64)
		if parseErr != nil || sequence == 0 || sequence != e.SourceSequence || e.Stage != in.State ||
			e.RequestID != in.RequestID || in.TurnRef != nil && *in.TurnRef != e.TurnRef ||
			(in.State == "turn_started" || in.State == "turn_completed") && in.TurnRef == nil ||
			in.InputID != nil && *in.InputID != e.InputID || in.InputID == nil && e.InputID != "" || e.EffectDigest != out.Intent.EffectDigest ||
			proof.Identity.RuntimeEpoch != b.RuntimeEpoch || !nativereceipts.ValidDigest(proof.Identity.ProcessIdentityDigest) ||
			proof.Identity.ProcessIdentityDigest != out.Intent.ProcessIdentityDigest || e.ObservedAt.IsZero() {
			return out, currentFailure(nativecontract.ReasonInputOutcomeUnconfirmed)
		}
	}
	if in.State == "exit_confirmed" {
		exit := proof.Exit
		if in.Operation != "stop" || exit == nil || !exit.RootExited || !exit.GroupAbsent || exit.ObservedAt.IsZero() ||
			!nativereceipts.ValidDigest(out.Intent.ProcessIdentityDigest) || exit.ProcessIdentityDigest != out.Intent.ProcessIdentityDigest ||
			proof.Identity.RuntimeEpoch != b.RuntimeEpoch || proof.Identity.ProcessIdentityDigest != exit.ProcessIdentityDigest {
			return out, currentFailure(nativecontract.ReasonOwnershipUnconfirmed)
		}
	}
	out.OwnerPersonID = s.OwnerPersonID
	return out, nil
}

func (a CurrentAuthority) ReadTx(ctx context.Context, tx pgx.Tx, actor nativecontract.Actor, b nativecontract.Binding) error {
	p, err := principal(ctx, actor)
	if err != nil {
		return err
	}
	s, err := a.Consent.SessionTx(ctx, tx, b)
	if err != nil {
		return currentError(err)
	}
	if p.Kind != tenant.Person || p.ID != s.Original.ID {
		return currentFailure(nativecontract.ReasonPermissionRevoked)
	}
	return nil
}

func (a CurrentAuthority) FinalTx(ctx context.Context, tx pgx.Tx, daemon nativecontract.DaemonActor, in nativecontract.FinalPublish) (FinalAuthority, error) {
	var out FinalAuthority
	b := in.ExpectedBinding
	s, err := a.Consent.SessionTx(ctx, tx, b)
	if err != nil {
		return out, currentError(err)
	}
	out.Sender, _, err = a.pairedTx(ctx, tx, daemon, b, s, true)
	if err != nil {
		return out, err
	}
	var raw []byte
	var grant, retention, owner string
	var revision int64
	err = tx.QueryRow(ctx, `SELECT binding,actor_grant_id::text,actor_grant_revision,retention_grant_id::text,
 owner_person_id::text,conversation_ref,conversation_id::text,role_id::text,issued_at,expires_at,maximum_finals,maximum_bytes
 FROM native_session_drain_grants WHERE tenant_id=$1 AND grant_id=$2 AND owned_session_id=$3 AND project_id=$4 FOR SHARE`, b.TenantID, in.DrainGrantID, b.OwnedSessionID, b.ProjectID).Scan(&raw, &grant, &revision, &retention, &owner, &out.ConversationRef,
		&out.Target.ConversationID, &out.Target.RoleID, &out.IssuedAt, &out.Grant.ExpiresAt, &out.Grant.MaximumFinals, &out.Grant.MaximumBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, currentFailure(nativecontract.ReasonPermissionRevoked)
	}
	if err != nil {
		return out, err
	}
	if len(raw) > 4096 || json.Unmarshal(raw, &out.Grant.Binding) != nil || !out.Grant.Binding.Equal(b) ||
		grant != s.GrantID || revision != s.Revision || owner != s.OwnerPersonID || out.ConversationRef != s.View.ConversationRef {
		return out, currentFailure(nativecontract.ReasonPermissionRevoked)
	}
	if _, err = a.Consent.RequireRetentionTx(ctx, tx, s.Actor, s.Intent, s.Current, retention); err != nil {
		return out, currentError(err)
	}
	out.Grant.GrantID, out.Grant.ConversationRef = in.DrainGrantID, out.ConversationRef
	out.CurrentBinding = b
	out.OriginalActor = s.Original
	out.Target.OwnerPersonID, out.Target.SessionID = owner, b.HarnessSessionID
	out.RetentionApproved = true
	return out, nil
}

// RegisterOperationTx is entered by the original person's command transaction,
// before its event. Only immutable content-free metadata is retained here.
func (a CurrentAuthority) RegisterOperationTx(ctx context.Context, tx pgx.Tx, in nativereceipts.Intent) error {
	if in.Receipt.Binding == nil || !workorders.UUID(in.Receipt.OperationID) || !nativereceipts.ValidDigest(in.PayloadSHA256) ||
		in.Receipt.State != "accepted" || in.Receipt.SourceSequence != "0" || in.Receipt.Reason != nil {
		return currentFailure(nativecontract.ReasonInvalidInput)
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return err
	}
	if err = nativereceipts.Validate(v, "NativeReceipt", in.Receipt); err != nil {
		return err
	}
	s, err := a.Consent.SessionTx(ctx, tx, *in.Receipt.Binding)
	if err != nil {
		return currentError(err)
	}
	p, ok := tenant.PrincipalFrom(ctx)
	if !ok || p.Kind != tenant.Person || p.TenantID != s.Original.TenantID || p.ID != s.Original.ID || in.ActorID != p.ID ||
		in.ExpiresAt.After(s.Policy.ExpiresAt) || in.ExpiresAt.IsZero() {
		return currentFailure(nativecontract.ReasonPermissionRevoked)
	}
	if in.Receipt.Operation != "input" && in.Receipt.Operation != "interrupt" && in.Receipt.Operation != "stop" {
		return currentFailure(nativecontract.ReasonUnsupported)
	}
	if in.Receipt.Operation == "input" && in.Receipt.InputID == nil || !nativereceipts.ValidDigest(in.ProcessIdentityDigest) {
		return currentFailure(nativecontract.ReasonInvalidInput)
	}
	if len(in.Reports) != 0 || in.ReportOffset != 0 || in.ReportAttempts != 0 {
		return currentFailure(nativecontract.ReasonInvalidInput)
	}
	in.Reports = []nativecontract.Receipt{}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if len(raw) > 16384 {
		return currentFailure(nativecontract.ReasonBufferFull)
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM native_session_operations WHERE tenant_id=$1 AND owned_session_id=$2 AND operation_id<>$3 LIMIT 513) x`, p.TenantID, in.Receipt.Binding.OwnedSessionID, in.Receipt.OperationID).Scan(&count); err != nil {
		return err
	}
	if count >= nativereceipts.MaxReceipts {
		return currentFailure(nativecontract.ReasonBufferFull)
	}
	_, err = tx.Exec(ctx, `INSERT INTO native_session_operations(tenant_id,operation_id,owned_session_id,project_id,owner_person_id,intent,created_at) VALUES($1,$2,$3,$4,$5,$6,clock_timestamp()) ON CONFLICT DO NOTHING`, p.TenantID, in.Receipt.OperationID, in.Receipt.Binding.OwnedSessionID, in.Receipt.Binding.ProjectID, s.OwnerPersonID, raw)
	if err != nil {
		return err
	}
	var saved []byte
	if err = tx.QueryRow(ctx, `SELECT intent FROM native_session_operations WHERE tenant_id=$1 AND operation_id=$2`, p.TenantID, in.Receipt.OperationID).Scan(&saved); err != nil {
		return err
	}
	var before nativereceipts.Intent
	if len(saved) > 16384 || json.Unmarshal(saved, &before) != nil {
		return currentFailure(nativecontract.ReasonIdentityMismatch)
	}
	canonical, _ := json.Marshal(before)
	if !bytes.Equal(canonical, raw) {
		return currentFailure(nativecontract.ReasonIdentityMismatch)
	}
	return nil
}

// IssueDrainTx pins an existing private chat target and separate retention
// approval. Retry reads the resulting ID; it never extends this window.
func (a CurrentAuthority) IssueDrainTx(ctx context.Context, tx pgx.Tx, b nativecontract.Binding, retentionID, conversation, role string) (nativecontract.FinalDrainGrant, error) {
	var out nativecontract.FinalDrainGrant
	s, err := a.Consent.SessionTx(ctx, tx, b)
	if err != nil {
		return out, currentError(err)
	}
	var now time.Time
	if a.Consent.Now != nil {
		now = a.Consent.Now()
	} else if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return out, err
	}
	p, ok := tenant.PrincipalFrom(ctx)
	if !ok || p.Kind != tenant.Person || p.ID != s.Original.ID || p.TenantID != b.TenantID {
		return out, currentFailure(nativecontract.ReasonPermissionRevoked)
	}
	retention, err := a.Consent.RequireRetentionTx(ctx, tx, s.Actor, s.Intent, s.Current, retentionID)
	if err != nil {
		return out, currentError(err)
	}
	var exact bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat_session_contexts c JOIN chat_threads t ON t.tenant_id=c.tenant_id AND t.role_id=c.role_id AND t.project_id=c.project_id AND t.person_id=c.owner_person_id WHERE c.tenant_id=$1 AND c.session_id=$2 AND c.owner_person_id=$3 AND c.project_id=$4 AND t.id=$5 AND c.role_id=$6 AND t.archived_at IS NULL)`, b.TenantID, b.HarnessSessionID, s.OwnerPersonID, b.ProjectID, conversation, role).Scan(&exact)
	if err != nil {
		return out, err
	}
	if !exact {
		return out, currentFailure(nativecontract.ReasonPermissionRevoked)
	}
	expiry := now.Add(nativereceipts.MaxRetention)
	for _, until := range []time.Time{s.Policy.ExpiresAt, retention.Policy.ExpiresAt} {
		if until.Before(expiry) {
			expiry = until
		}
	}
	if !now.Before(expiry) {
		return out, currentFailure(nativecontract.ReasonPublicationGrantExpired)
	}
	var prior []byte
	err = tx.QueryRow(ctx, `SELECT grant_id::text,binding,conversation_ref,expires_at,maximum_finals,maximum_bytes FROM native_session_drain_grants WHERE tenant_id=$1 AND owned_session_id=$2 AND retention_grant_id=$3 AND conversation_id=$4 AND role_id=$5`, b.TenantID, b.OwnedSessionID, retentionID, conversation, role).Scan(&out.GrantID, &prior, &out.ConversationRef, &out.ExpiresAt, &out.MaximumFinals, &out.MaximumBytes)
	if err == nil {
		if len(prior) > 4096 || json.Unmarshal(prior, &out.Binding) != nil || !out.Binding.Equal(b) || !now.Before(out.ExpiresAt) {
			return nativecontract.FinalDrainGrant{}, currentFailure(nativecontract.ReasonPublicationGrantExpired)
		}
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	out = nativecontract.FinalDrainGrant{GrantID: uuid.NewString(), Binding: b, ConversationRef: s.View.ConversationRef, ExpiresAt: expiry, MaximumFinals: nativereceipts.MaxFinals, MaximumBytes: nativereceipts.MaxFinalBytes}
	raw, _ := json.Marshal(b)
	_, err = tx.Exec(ctx, `INSERT INTO native_session_drain_grants(tenant_id,grant_id,owned_session_id,project_id,owner_person_id,actor_grant_id,actor_grant_revision,retention_grant_id,binding,conversation_ref,conversation_id,role_id,issued_at,expires_at,maximum_finals,maximum_bytes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, b.TenantID, out.GrantID, b.OwnedSessionID, b.ProjectID, s.OwnerPersonID, s.GrantID, s.Revision, retentionID, raw, out.ConversationRef, conversation, role, now, expiry, out.MaximumFinals, out.MaximumBytes)
	return out, err
}

// RuntimeTx revalidates the exact original consent and current paired lease
// for the owning composition. It issues no launch or person authority.
func (a CurrentAuthority) RuntimeTx(ctx context.Context, tx pgx.Tx, d nativecontract.DaemonActor, b nativecontract.Binding, draining bool) (consent.Session, RuntimeProof, error) {
	s, err := a.Consent.SessionTx(ctx, tx, b)
	if err != nil {
		return s, RuntimeProof{}, currentError(err)
	}
	_, proof, err := a.pairedTx(ctx, tx, d, b, s, draining)
	return s, proof, err
}

// AbandonTx authenticates only the exact paired owner and worker lease of a
// claimed binding. Verified no-fork abandonment needs no current person consent
// (its withdrawal is a cause): it settles occupancy and grants no launch, input
// or final. A settled retry finds its harness stopped, so liveness is not read.
func (a CurrentAuthority) AbandonTx(ctx context.Context, tx pgx.Tx, d nativecontract.DaemonActor, b nativecontract.Binding, intent nativecontract.StartIntent, view nativecontract.SessionView) error {
	if d.DaemonID != b.DaemonID || d.DaemonGeneration != b.DaemonGeneration {
		return currentFailure(nativecontract.ReasonPermissionRevoked)
	}
	_, _, err := a.pairedTx(ctx, tx, d, b, consent.Session{Intent: intent, View: view}, true)
	return err
}
