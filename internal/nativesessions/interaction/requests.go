// SPDX-License-Identifier: AGPL-3.0-only

package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativeinteraction"
	"github.com/inspr-at/paimos/internal/questions"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Request routes filtered canUseTool/app-server question metadata through the
// existing desk. The exact binding/request/turn/effect in Context prevents
// matching or reusing a prior Always answer. No tool arguments are persisted.
func (s *Store) Request(ctx context.Context, daemon nativecontract.DaemonActor, in nativecontract.Approval) (nativecontract.Approval, error) {
	var out nativecontract.Approval
	if len(in.Summary) > 2048 || len(in.AllowedDecisions) > 3 {
		return out, fault(413, nativecontract.ReasonBufferFull)
	}
	if err := nativeinteraction.Validate(s.validator, "NativeApproval", in); err != nil {
		return out, err
	}
	if in.DeskItemID != nil || !nativeinteraction.ValidChoices(in) || in.ExpiresInMS <= 0 {
		return out, fault(400, nativecontract.ReasonInvalidInput)
	}
	err := s.transaction(ctx, daemon.Actor, in.Binding, "request:"+in.Kind, func(ctx context.Context, tx pgx.Tx, p tenant.Principal, l *locked) error {
		if p.Kind != tenant.Agent || p.ID != l.paired || daemon.ComputerID != l.computer || daemon.DaemonID != in.Binding.DaemonID {
			return fault(403, nativecontract.ReasonWrongPrincipal)
		}
		if daemon.DaemonGeneration != in.Binding.DaemonGeneration || daemon.LedgerGeneration != l.ledger {
			return fault(409, nativecontract.ReasonWrongGeneration)
		}
		if err := require(ctx, tx, p, in.Binding, "harness.worker", "questions.ask", "questions.read"); err != nil {
			return err
		}
		if in.Kind == "question" && !l.view.Capabilities.Questions || in.Kind != "question" && !l.view.Capabilities.Approvals {
			return fault(400, nativecontract.ReasonUnsupported)
		}
		// Private proof/current native turn is checked again here, not inferred
		// from the daemon key or producer-controlled correlation fields.
		if err := s.authority.AuthorizeTx(ctx, tx, p, in.Binding, "native_request:"+in.TurnRef+":"+in.NativeRequestRef+":"+in.EffectDigest); err != nil {
			return err
		}
		stepUp, err := s.authority.AuthorizeRequestTx(ctx, tx, p, in)
		if err != nil {
			return err
		}
		in.StepUpRequired = in.StepUpRequired || stepUp
		if err := s.refresh(ctx, tx, l); err != nil {
			return err
		}
		var oldID, boot, desk string
		var oldDigest []byte
		var expires time.Time
		var resolved *time.Time
		err = tx.QueryRow(ctx, `SELECT id::text,request_digest,boot_id::text,desk_item_id::text,expires_at,resolved_at FROM native_session_interactions WHERE request_id=$1`, in.RequestID).Scan(&oldID, &oldDigest, &boot, &desk, &expires, &resolved)
		if err == nil {
			if oldID != in.ApprovalID || !sameDigest(oldDigest, digest(in)) {
				return fault(409, nativecontract.ReasonIdentityMismatch)
			}
			if boot != s.bootID || resolved != nil || !l.now.Before(expires) {
				return fault(410, nativecontract.ReasonApprovalExpired)
			}
			out = in
			out.DeskItemID = &desk
			out.ExpiresAt = expires
			out.ExpiresInMS = expires.Sub(l.now).Milliseconds()
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM native_session_interactions WHERE owned_session_id=$1 AND boot_id=$2 AND binding_digest=$3 AND expires_at>$4 AND resolved_at IS NULL`, in.Binding.OwnedSessionID, s.bootID, digest(in.Binding), l.now).Scan(&count); err != nil {
			return err
		}
		if count >= 32 {
			return fault(429, nativecontract.ReasonBufferFull)
		}
		expires = minTime(l.now.Add(time.Duration(in.ExpiresInMS)*time.Millisecond), l.policy.ExpiresAt)
		// Insert our isolated row before AskTx appends desk events. After that
		// call we update only rows already owned here (this row and binding),
		// and append the native event last. No new lock follows the counter.
		_, err = tx.Exec(ctx, `INSERT INTO native_session_interactions(tenant_id,id,owned_session_id,actor_person_id,requester_principal_id,request_id,request_digest,binding_digest,boot_id,turn_ref,native_request_ref,kind,effect_digest,step_up_required,expires_at,created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, p.TenantID, in.ApprovalID, in.Binding.OwnedSessionID, l.view.OwnerPersonID, p.ID, in.RequestID, digest(in), digest(in.Binding), s.bootID, in.TurnRef, in.NativeRequestRef, in.Kind, in.EffectDigest, in.StepUpRequired, expires, l.now)
		if err != nil {
			return err
		}
		q, _, err := s.desk.AskTx(ctx, tx, p, in.Binding.ProjectID, deskInput(in))
		if err != nil {
			return err
		}
		if q.State != "open" || q.Answer != nil {
			return fault(409, nativecontract.ReasonIdentityMismatch)
		}
		if err = s.refresh(ctx, tx, l); err != nil {
			return err
		}
		if !l.now.Before(expires) {
			return fault(410, nativecontract.ReasonApprovalExpired)
		}
		if _, err = tx.Exec(ctx, `UPDATE native_session_interactions SET desk_item_id=$2 WHERE id=$1`, in.ApprovalID, q.ID); err != nil {
			return err
		}
		if err = s.saveView(ctx, tx, l); err != nil {
			return err
		}
		out = in
		out.DeskItemID = &q.ID
		out.ExpiresAt = expires
		out.ExpiresInMS = expires.Sub(l.now).Milliseconds()
		return s.event(ctx, tx, p, l, in.RequestID, "native_session.approval_changed", "pending")
	})
	if err != nil {
		return nativecontract.Approval{}, err
	}
	return out, nil
}

func deskInput(a nativecontract.Approval) questions.Input {
	contextRaw, _ := json.Marshal(struct {
		Binding       nativecontract.Binding `json:"binding"`
		RequestID     string                 `json:"request_id"`
		ApprovalID    string                 `json:"approval_id"`
		Turn          string                 `json:"turn_ref"`
		NativeRequest string                 `json:"native_request_ref"`
		Effect        string                 `json:"effect_digest"`
	}{a.Binding, a.RequestID, a.ApprovalID, a.TurnRef, a.NativeRequestRef, a.EffectDigest})
	options := []questions.Option{{ID: "allow_once", Title: "Allow once", Answer: "allow_once"}, {ID: "deny", Title: "Deny", Answer: "deny"}}
	if a.Kind == "question" {
		options = []questions.Option{{ID: "answer", Title: "Answer this request", Answer: "Enter the exact answer"}, {ID: "deny", Title: "Deny", Answer: "deny"}}
	}
	return questions.Input{RequestID: a.RequestID, Question: a.Summary, Context: string(contextRaw), Options: options, SessionID: a.Binding.HarnessSessionID, Meanwhile: "paused", SuggestedOutcome: "once"}
}

// Resolution is local-only delivery context. Accepted is a decision recorded
// by the server, never evidence that a vendor applied it. I2/P3 queue/claim the
// bounded command and report native evidence; Broker performs the final fence.
type Resolution struct {
	Approval nativecontract.Approval
	Decision nativecontract.ApprovalDecision
	Replay   bool
}

func (s *Store) Decide(ctx context.Context, a nativecontract.Actor, in nativecontract.ApprovalDecision) (Resolution, error) {
	var out Resolution
	if in.Answer != nil && len(*in.Answer) > 8192 {
		return out, fault(413, nativecontract.ReasonBufferFull)
	}
	if err := nativeinteraction.Validate(s.validator, "NativeApprovalDecision", in); err != nil {
		return out, err
	}
	// Denials require a person too. Owner-workstation agents never acquire
	// human authority here, even for another agent's request.
	p, err := principal(ctx, a)
	if err != nil {
		return out, err
	}
	if err = human(p); err != nil {
		return out, err
	}
	err = s.transaction(ctx, a, in.ExpectedBinding, "decision", func(ctx context.Context, tx pgx.Tx, p tenant.Principal, l *locked) error {
		if err := require(ctx, tx, p, in.ExpectedBinding, "questions.read", "questions.decide"); err != nil {
			return err
		}
		var requester, boot, desk string
		var bound, oldDigest []byte
		var decisionRequest, decidedBy *string
		var created time.Time
		var resolved *time.Time
		approval := nativecontract.Approval{ApprovalID: in.ApprovalID, Binding: in.ExpectedBinding}
		err := tx.QueryRow(ctx, `SELECT requester_principal_id::text,boot_id::text,binding_digest,request_id::text,turn_ref,native_request_ref,kind,effect_digest,desk_item_id::text,step_up_required,expires_at,created_at,decision_request_id::text,decision_digest,decided_by::text,resolved_at
 FROM native_session_interactions WHERE id=$1 AND owned_session_id=$2 FOR NO KEY UPDATE`, in.ApprovalID, in.ExpectedBinding.OwnedSessionID).Scan(&requester, &boot, &bound, &approval.RequestID, &approval.TurnRef, &approval.NativeRequestRef, &approval.Kind, &approval.EffectDigest, &desk, &approval.StepUpRequired, &approval.ExpiresAt, &created, &decisionRequest, &oldDigest, &decidedBy, &resolved)
		if errors.Is(err, pgx.ErrNoRows) {
			return fault(410, nativecontract.ReasonApprovalExpired)
		}
		if err != nil {
			return err
		}
		if boot != s.bootID || !sameDigest(bound, digest(in.ExpectedBinding)) || !l.now.Before(approval.ExpiresAt) {
			return fault(410, nativecontract.ReasonApprovalExpired)
		}
		if approval.EffectDigest != in.EffectDigest {
			return fault(409, nativecontract.ReasonIdentityMismatch)
		}
		if p.ID == requester {
			return fault(403, nativecontract.ReasonWrongPrincipal)
		}
		approval.DeskItemID = &desk
		approval.ExpiresInMS = approval.ExpiresAt.Sub(l.now).Milliseconds()
		approval.AllowedDecisions = []string{"allow_once", "deny"}
		if approval.Kind == "question" {
			approval.AllowedDecisions = []string{"answer", "deny"}
		}
		if !slices.Contains(approval.AllowedDecisions, in.Decision) {
			return fault(400, nativecontract.ReasonUnsupported)
		}
		if err = s.authority.AuthorizeTx(ctx, tx, p, in.ExpectedBinding, "native_decision:"+approval.TurnRef+":"+approval.NativeRequestRef+":"+approval.EffectDigest); err != nil {
			return err
		}
		if err = s.refresh(ctx, tx, l); err != nil {
			return err
		}
		if !l.now.Before(approval.ExpiresAt) {
			return fault(410, nativecontract.ReasonApprovalExpired)
		}
		// Read the exact current human answer, rather than treating an inbox
		// reply or a reused/Always decision as a native tool authorization.
		var answer, option, outcome, actor string
		var answeredAt time.Time
		var questionInput questions.Input
		err = tx.QueryRow(ctx, `SELECT a.answer,a.option_id,a.outcome,a.decided_by::text,a.created_at,s.input
 FROM desk_questions q JOIN nodes n ON n.tenant_id=q.tenant_id AND n.id=q.node_id
 JOIN desk_answers a ON a.tenant_id=q.tenant_id AND a.question_id=q.node_id AND a.revision=q.revision
 JOIN nodes an ON an.tenant_id=a.tenant_id AND an.id=a.node_id
 JOIN principals human ON human.tenant_id=a.tenant_id AND human.id=a.decided_by
 JOIN desk_askers s ON s.tenant_id=q.tenant_id AND s.question_id=q.node_id
 WHERE q.node_id=$1 AND q.project_id=$2 AND q.state='answered' AND n.deleted_at IS NULL AND an.deleted_at IS NULL
 AND human.kind='person' AND human.status='active' AND s.request_id=$3 AND s.principal_id=$4 AND s.session_id=$5 AND s.reused_decision_id IS NULL
 FOR SHARE OF q`, desk, in.ExpectedBinding.ProjectID, approval.RequestID, requester, in.ExpectedBinding.HarnessSessionID).Scan(&answer, &option, &outcome, &actor, &answeredAt, &questionInput)
		if errors.Is(err, pgx.ErrNoRows) {
			return fault(403, nativecontract.ReasonGrantMissing)
		}
		if err != nil {
			return err
		}
		// Summary lives solely in the existing authorized desk content store.
		approval.Summary = questionInput.Question
		if actor != p.ID || outcome != "once" || answeredAt.Before(created) || !answeredAt.Before(approval.ExpiresAt) || questionInput.Context != deskInput(approval).Context {
			return fault(403, nativecontract.ReasonGrantMissing)
		}
		if in.Decision == "answer" {
			if in.Answer == nil || *in.Answer != answer || option != "answer" {
				return fault(409, nativecontract.ReasonIdentityMismatch)
			}
		} else if answer != in.Decision || option != in.Decision {
			return fault(409, nativecontract.ReasonIdentityMismatch)
		}
		if resolved != nil {
			if decidedBy == nil || *decidedBy != p.ID || decisionRequest == nil || *decisionRequest != in.RequestID || !sameDigest(oldDigest, digest(in)) {
				return fault(409, nativecontract.ReasonIdentityMismatch)
			}
			out = Resolution{Approval: approval, Decision: in, Replay: true}
			return nil // an idempotent acknowledgement cannot execute the callback
		}
		if l.view.Revision != in.ExpectedRevision {
			return fault(409, nativecontract.ReasonStaleRevision)
		}
		if approval.StepUpRequired && in.Decision != "deny" {
			if s.stepUp == nil || in.StepUpAuthorizationID == nil {
				return fault(428, nativecontract.ReasonGrantMissing)
			}
			if err = s.stepUp.VerifyTx(ctx, tx, p, approval, in); err != nil {
				return err
			}
		} else if in.StepUpAuthorizationID != nil {
			return fault(400, nativecontract.ReasonInvalidInput)
		}
		if err = s.refresh(ctx, tx, l); err != nil {
			return err
		}
		if !l.now.Before(approval.ExpiresAt) {
			return fault(410, nativecontract.ReasonApprovalExpired)
		}
		_, err = tx.Exec(ctx, `UPDATE native_session_interactions SET decision_request_id=$2,decision_digest=$3,decided_by=$4,resolved_at=$5 WHERE id=$1`, in.ApprovalID, in.RequestID, digest(in), p.ID, l.now)
		if err != nil {
			return err
		}
		if err = s.saveView(ctx, tx, l); err != nil {
			return err
		}
		out = Resolution{Approval: approval, Decision: in}
		return s.event(ctx, tx, p, l, in.RequestID, "native_session.approval_changed", "resolved")
	})
	if err != nil {
		return Resolution{}, err
	}
	return out, nil
}
