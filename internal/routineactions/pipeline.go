// SPDX-License-Identifier: AGPL-3.0-only
package routineactions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/routineactions/evidence"
	"github.com/inspr-at/paimos/internal/routineguard"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type PipelineRequest struct {
	NonCode      bool   `json:"non_code"`
	PRActionKey  string `json:"pr_action_key,omitempty"`
	HeadSHA      string `json:"head_sha,omitempty"`
	ResultDigest string `json:"result_digest,omitempty"`
}

func (v PipelineRequest) valid() bool {
	if v.NonCode {
		return v.PRActionKey == "" && v.HeadSHA == "" && approvals.RoutineScope("work_orders.write", v.ResultDigest) != ""
	}
	return actionKey.MatchString(v.PRActionKey) && v.PRActionKey != "occurrence" && reviewgate.ValidSHA(v.HeadSHA) && v.ResultDigest == ""
}

type Pipeline interface {
	RoutineSnapshot(context.Context, string, string, string, string) (evidence.PipelineFacts, error)
	RoutineReadyTx(context.Context, pgx.Tx, string, string, string, evidence.PipelineFacts) (string, bool, error)
	RoutineReviewTx(context.Context, pgx.Tx, string, string) (evidence.Review, error)
	RoutineMergeResultTx(context.Context, pgx.Tx, string, string, string, string, int64) (bool, string, error)
}

func (b *Broker) WithObservation(observe ObserveTx) *Broker { b.observe = observe; return b }

// pipelineSettledTx checks durable process/usage/catalogue lineage before a
// person handoff. A request never gives its worker merge/deploy authority.
func pipelineSettledTx(ctx context.Context, tx pgx.Tx, run, id string, nonCode bool) error {
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM routine_runs WHERE id=$1`, run).Scan(&state); err != nil {
		return err
	}
	if state != "awaiting_pipeline" {
		return workorders.Fail(409, "coordinator_pipeline_handoff_required")
	}
	var pending int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT id FROM routine_actions WHERE run_id=$1 AND id<>$2 AND state<>'succeeded' LIMIT 101) a`, run, id).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return workorders.Fail(409, "catalogue_actions_unsettled")
	}
	var approvalPending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_actions a WHERE a.run_id=$1 AND a.id<>$2 AND a.approval_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM agent_permission_grants g JOIN approval_decisions d ON d.request_id=g.approval_request_id WHERE g.approval_request_id=a.approval_id AND g.revoked_at IS NULL AND g.valid_until>clock_timestamp() AND d.decision='approved'))`, run, id).Scan(&approvalPending); err != nil {
		return err
	}
	if approvalPending {
		return workorders.Fail(409, "catalogue_approvals_unsettled")
	}
	var unsettled, noExit, code bool
	err := tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM routine_budget_grants WHERE run_id=$1 AND state<>'settled'),
 NOT EXISTS(SELECT 1 FROM routine_attempts WHERE run_id=$1 AND role='build' AND state='completed' AND process_receipt->>'process_state'='exited') OR EXISTS(SELECT 1 FROM routine_attempts WHERE run_id=$1 AND (state NOT IN ('completed','failed') OR process_receipt->>'process_state' IS DISTINCT FROM 'exited')),
 EXISTS(SELECT 1 FROM routine_attempts WHERE run_id=$1 AND role IN ('review','fix')) OR EXISTS(SELECT 1 FROM routine_actions WHERE run_id=$1 AND kind='pr.open')`, run).Scan(&unsettled, &noExit, &code)
	if err != nil {
		return err
	}
	if unsettled || noExit {
		return workorders.Fail(409, "pipeline_exit_or_usage_unconfirmed")
	}
	if nonCode && code {
		return workorders.Fail(409, "code_result_requires_pr_pipeline")
	}
	return nil
}

func loadPRActionTx(ctx context.Context, tx pgx.Tx, run, key string) (PROpen, Receipt, error) {
	var payload, result []byte
	err := tx.QueryRow(ctx, `SELECT o.payload,a.result FROM routine_actions a JOIN routine_effect_outbox o ON o.action_id=a.id AND o.run_id=a.run_id WHERE a.run_id=$1 AND a.action_key=$2 AND a.kind='pr.open' AND a.state='succeeded'`, run, key).Scan(&payload, &result)
	if errors.Is(err, pgx.ErrNoRows) {
		return PROpen{}, Receipt{}, workorders.Fail(409, "pr_receipt_pending")
	}
	if err != nil {
		return PROpen{}, Receipt{}, err
	}
	var e effect
	var r Receipt
	if json.Unmarshal(payload, &e) != nil || json.Unmarshal(result, &r) != nil || e.Request.PROpen == nil || !e.Request.PROpen.Valid() || r.PullRequest < 1 || r.OperationRef != e.OperationRef || r.RequestDigest != routineguard.Digest(e.Request) {
		return PROpen{}, Receipt{}, workorders.Fail(409, "pipeline_pr_binding_changed")
	}
	return *e.Request.PROpen, r, nil
}

// ReconcilePipeline reads external facts outside transactions, then rechecks
// consent, evaluation, permissions, review and exact approval in the final
// write. Changed queue/artifact/check/policy evidence requires a new decision.
func (b *Broker) ReconcilePipeline(ctx context.Context, tid, id string) (Receipt, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	if out, done, err := b.observeMerge(ctx, tid, id); err != nil || done {
		return out, err
	}
	var request effect
	var out, pr Receipt
	var in PROpen
	var review evidence.Review
	err := b.effectTx(ctx, tid, id, func(tx pgx.Tx, _ tenant.Principal, _ Binding, e effect, _ string, r *Receipt) error {
		request, out = e, *r
		if e.Request.PipelineRequest == nil {
			return workorders.Fail(409, "pipeline_action_required")
		}
		if err := pipelineSettledTx(ctx, tx, r.RunID, r.ID, e.Request.PipelineRequest.NonCode); err != nil {
			return err
		}
		if !e.Request.PipelineRequest.NonCode {
			var err error
			in, pr, err = loadPRActionTx(ctx, tx, r.RunID, e.Request.PipelineRequest.PRActionKey)
			if err != nil {
				return err
			}
			if b.pipeline == nil {
				return nil
			}
			review, err = b.pipeline.RoutineReviewTx(ctx, tx, r.RunID, e.Request.TargetID)
			if err != nil {
				return err
			}
			// The coordinator's completed handoff may contain a fix head.
			// The original request and PR-open send evidence stay immutable.
			if reviewgate.ValidSHA(review.HeadSHA) && review.Repository == in.Repository {
				in.HeadSHA = review.HeadSHA
			}
			return nil
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	var facts evidence.PipelineFacts
	unavailable := false
	if !request.Request.PipelineRequest.NonCode {
		if b.pipeline == nil {
			unavailable = true
		} else {
			facts, err = b.pipeline.RoutineSnapshot(ctx, tid, in.Repository, in.Branch, in.HeadSHA)
			unavailable = err != nil
		}
	}
	err = b.effectTx(ctx, tid, id, func(tx pgx.Tx, p tenant.Principal, binding Binding, e effect, _ string, r *Receipt) error {
		if err := pipelineSettledTx(ctx, tx, r.RunID, r.ID, e.Request.PipelineRequest.NonCode); err != nil {
			return err
		}
		r.State, r.WaitReason = "pending", ""
		wait := func(reason string) error { r.WaitReason = reason; out = *r; return nil }
		proof := map[string]any{"action": r.RequestDigest, "assignment": binding.AssignmentDigest, "result": e.Request.PipelineRequest.ResultDigest, "target": e.Request.TargetID, "revision": e.Request.ExpectedRevision}
		if !e.Request.PipelineRequest.NonCode {
			current, currentPR, err := loadPRActionTx(ctx, tx, r.RunID, e.Request.PipelineRequest.PRActionKey)
			if err != nil {
				return err
			}
			v := review
			if b.pipeline != nil {
				v, err = b.pipeline.RoutineReviewTx(ctx, tx, r.RunID, e.Request.TargetID)
				if err != nil {
					return err
				}
				if v != review {
					return wait("pipeline_review_stale")
				}
				current.HeadSHA = in.HeadSHA
			}
			if current != in || currentPR.ID != pr.ID {
				return workorders.Fail(409, "pipeline_pr_binding_changed")
			}
			now := time.Now().UTC()
			if unavailable || facts.ObservedAt.After(now) || now.Sub(facts.ObservedAt) > time.Minute {
				return wait("pipeline_facts_unavailable")
			}
			f := facts.Facts
			if f.PR == nil || *f.PR != pr.PullRequest || f.PullHead != in.HeadSHA {
				return wait("pipeline_head_changed")
			}
			if f.Merged {
				done, reason, err := b.completeObservedMergeTx(ctx, tx, p, binding, e, r, facts.MergeSHA)
				if err != nil {
					return err
				}
				if done {
					out = *r
					return nil
				}
				return wait(reason)
			}
			if b.pipeline == nil {
				return wait("coordinator_review_unavailable")
			}
			order := v.OrderID
			if !v.GateOpen || v.Verdict != "ok" || v.HeadSHA != in.HeadSHA || v.BaseSHA != f.Base || v.Repository != in.Repository {
				return wait("pipeline_review_stale")
			}
			s, ready, err := b.pipeline.RoutineReadyTx(ctx, tx, binding.ProjectID, in.Repository, in.HeadSHA, facts)
			if err != nil {
				return err
			}
			if !ready {
				return wait("pipeline_ci_or_artifact_pending")
			}
			proof["pr"], proof["head"], proof["base"], proof["artifact"], proof["queue_head"], proof["review"], proof["checks"], proof["runs"], proof["settings"] = pr.PullRequest, in.HeadSHA, f.Base, facts.ArtifactSHA, f.QueueHead, order, f.Checks, f.Runs, s
			proof["review_policy"] = v.PolicyDigest
			r.PullRequest, r.PullURL = pr.PullRequest, pr.PullURL
		}
		var policy, eval string
		if err := tx.QueryRow(ctx, `SELECT policy_digest,evaluation_id::text FROM routine_actions WHERE id=$1`, r.ID).Scan(&policy, &eval); err != nil {
			return err
		}
		proof["guard_policy"], proof["evaluation"] = policy, eval
		digest := routineguard.Digest(proof)
		now := b.now().UTC()
		var pending []events.Change
		detail := fmt.Sprintf("Routine run /agents?tab=routines&run=%s; action %s. Normal pipeline only; merge, publication and deployment remain person/OPS-owned.", r.RunID, r.ID)
		if r.PullURL != "" {
			detail += " PR: " + r.PullURL
		}
		a, err := approvals.RequestRoutineHoldTx(ctx, tx, p, e.Request.TargetID, "work_orders.write", digest, now.Add(24*time.Hour), now, &pending, detail)
		if err != nil {
			return err
		}
		r.ApprovalID = a.ID
		approved, err := approvals.LiveRoutineHoldTx(ctx, tx, p, a.ID, e.Request.TargetID, "work_orders.write", digest, now)
		if err != nil {
			return err
		}
		if !e.Request.PipelineRequest.NonCode {
			if err := rememberPipelineApprovalTx(ctx, tx, *r, e.Request.PipelineRequest.HeadSHA, pipelineApproval{ID: a.ID, Digest: digest, HeadSHA: in.HeadSHA, QueueHead: facts.Facts.QueueHead, Approved: approved}); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE routine_effect_outbox SET result=jsonb_build_object('approval_digest',$2::text) WHERE action_id=$1`, r.ID, digest); err != nil {
			return err
		}
		r.State = "awaiting_decision"
		if approved {
			if e.Request.PipelineRequest.NonCode {
				r.State = "succeeded"
				if err := completePipelineTx(ctx, tx, r.RunID, r.ID); err != nil {
					return err
				}
			} else {
				r.State = "awaiting_merge"
			}
		}
		// Persist before the approval events acquire the event counter.
		if err := saveReceiptTx(ctx, tx, *r); err != nil {
			return err
		}
		out = *r
		for _, change := range pending {
			if _, err := events.Append(ctx, tx, p, change); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// observeMerge handles the revision change made by AEON-1008 itself. It is
// observation of a committed exact receipt, not a new effect. Current owner,
// assignment and consent remain mandatory; no stale action revision authorizes
// a write to the work node. This path changes only routine receipts/state.
func (b *Broker) observeMerge(ctx context.Context, tid, id string) (Receipt, bool, error) {
	var out Receipt
	done := false
	err := db.InTenant(db.AllProjects(ctx, "observe routine main merge receipt"), b.pool, tid, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		var raw, result []byte
		var run string
		err := tx.QueryRow(ctx, `SELECT a.run_id::text,o.payload,a.result FROM routine_actions a JOIN routine_effect_outbox o ON o.action_id=a.id WHERE a.id=$1 AND a.kind='pipeline.request' AND a.state<>'succeeded' FOR NO KEY UPDATE OF a,o`, id).Scan(&run, &raw, &result)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var e effect
		if len(raw) > MaxBodyBytes || json.Unmarshal(raw, &e) != nil || json.Unmarshal(result, &out) != nil || e.Request.Validate() != nil || !workorders.UUID(e.PrincipalID) || e.Request.PipelineRequest == nil || out.ID != id || out.RunID != run || out.RequestDigest != routineguard.Digest(e.Request) || out.OperationRef != e.OperationRef || e.OperationRef != routineguard.Digest([]string{tid, run, e.Request.ActionKey, out.RequestDigest}) {
			return workorders.Fail(409, "pipeline_receipt_invalid")
		}
		if e.Request.PipelineRequest.NonCode {
			return nil
		}
		p := tenant.Principal{ID: e.PrincipalID, TenantID: tid, Kind: tenant.Agent}
		binding, err := loadBinding(ctx, tx, p, run, e.Request.AttemptID, true)
		if err != nil {
			return err
		}
		if b.observe == nil {
			return nil
		}
		actor, err := b.observe(ctx, tx, p, binding)
		if err != nil {
			return err
		}
		if actor.ID != binding.ExecutionPrincipalID || actor.TenantID != tid {
			return authz.ErrForbidden
		}
		p.Scopes = actor.Scopes
		for _, who := range []tenant.Principal{p, actor, {ID: binding.OwnerID, TenantID: tid, Kind: tenant.Person}} {
			if err := authz.RequireTx(ctx, tx, who, "work_orders.write", authz.Scope{ProjectID: binding.ProjectID}); err != nil {
				return err
			}
		}
		done, _, err = b.completeObservedMergeTx(ctx, tx, p, binding, e, &out, "")
		return err
	})
	return out, done, err
}

// Each immutable digest keeps its own head/queue evidence, including an
// approval that preceded queueing. A later pending request cannot erase it.
// Once a newer binding was approved, revocation cannot fall back to an older
// broad pre-queue decision. History is bounded before any approval work.
type pipelineApproval struct {
	ID        string `json:"id"`
	Digest    string `json:"digest"`
	HeadSHA   string `json:"head_sha"`
	QueueHead string `json:"queue_head"`
	Approved  bool   `json:"approved"`
}

type pipelineHistory struct {
	ApprovalDigest string             `json:"approval_digest"`
	QueueHead      string             `json:"queue_head"`
	HeadSHA        string             `json:"head_sha"`
	Approvals      []pipelineApproval `json:"approvals"`
}

const maxPipelineApprovals = 32

func loadPipelineHistoryTx(ctx context.Context, tx pgx.Tx, r Receipt, initialHead string) (pipelineHistory, error) {
	var h pipelineHistory
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT coalesce(result,'{}'::jsonb) FROM routine_effect_outbox WHERE action_id=$1`, r.ID).Scan(&raw); err != nil {
		return h, err
	}
	if len(raw) > 32768 || json.Unmarshal(raw, &h) != nil || len(h.Approvals) > maxPipelineApprovals {
		return h, workorders.Fail(409, "pipeline_approval_history_invalid")
	}
	// Existing unreleased receipts have one exact binding; retain it when
	// reconciling across the upgrade instead of making up new consent.
	if len(h.Approvals) == 0 && h.ApprovalDigest != "" {
		h.HeadSHA = initialHead
		h.Approvals = []pipelineApproval{{ID: r.ApprovalID, Digest: h.ApprovalDigest, HeadSHA: initialHead, QueueHead: h.QueueHead, Approved: r.State == "awaiting_merge"}}
	}
	for _, a := range h.Approvals {
		if !workorders.UUID(a.ID) || approvals.RoutineScope("work_orders.write", a.Digest) == "" || !reviewgate.ValidSHA(a.HeadSHA) || a.QueueHead != "" && !reviewgate.ValidSHA(a.QueueHead) {
			return h, workorders.Fail(409, "pipeline_approval_history_invalid")
		}
	}
	return h, nil
}

func savePipelineHistoryTx(ctx context.Context, tx pgx.Tx, id string, h pipelineHistory) error {
	raw, err := json.Marshal(h)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE routine_effect_outbox SET result=coalesce(result,'{}'::jsonb)||$2::jsonb WHERE action_id=$1`, id, raw)
	return err
}

func rememberPipelineApprovalTx(ctx context.Context, tx pgx.Tx, r Receipt, initialHead string, a pipelineApproval) error {
	h, err := loadPipelineHistoryTx(ctx, tx, r, initialHead)
	if err != nil {
		return err
	}
	for i, old := range h.Approvals {
		if old.Digest != a.Digest {
			continue
		}
		if old.HeadSHA != a.HeadSHA || old.QueueHead != a.QueueHead {
			return workorders.Fail(409, "pipeline_approval_binding_changed")
		}
		a.Approved = a.Approved || old.Approved
		h.Approvals[i] = a
		h.HeadSHA, h.ApprovalDigest, h.QueueHead = a.HeadSHA, a.Digest, a.QueueHead
		return savePipelineHistoryTx(ctx, tx, r.ID, h)
	}
	if len(h.Approvals) >= maxPipelineApprovals {
		return workorders.Fail(409, "pipeline_approval_history_full")
	}
	h.Approvals = append(h.Approvals, a)
	h.HeadSHA, h.ApprovalDigest, h.QueueHead = a.HeadSHA, a.Digest, a.QueueHead
	return savePipelineHistoryTx(ctx, tx, r.ID, h)
}

// completeObservedMergeTx is shared by receipt-only recovery and the merged
// GitHub branch. It consumes AEON-1008 evidence and a live exact person hold;
// it never changes a work node or uses a pending request as merge authority.
func (b *Broker) completeObservedMergeTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, binding Binding, e effect, r *Receipt, merge string) (bool, string, error) {
	if err := requireTargetState(ctx, tx, binding, e.Request, false); err != nil {
		return false, "", err
	}
	var work string
	if err := tx.QueryRow(ctx, `SELECT work_node_id::text FROM routine_runs WHERE id=$1`, r.RunID).Scan(&work); err != nil {
		return false, "", err
	}
	if work != e.Request.TargetID {
		return false, "", workorders.Fail(409, "external_action_target_changed")
	}
	in, pr, err := loadPRActionTx(ctx, tx, r.RunID, e.Request.PipelineRequest.PRActionKey)
	if err != nil {
		return false, "", err
	}
	h, err := loadPipelineHistoryTx(ctx, tx, *r, in.HeadSHA)
	if err != nil {
		return false, "", err
	}
	if len(h.Approvals) == 0 {
		return false, "pipeline_person_approval_or_observation_pending", nil
	}
	if err := pipelineSettledTx(ctx, tx, r.RunID, r.ID, false); err != nil {
		return false, "", err
	}
	if b.pipeline == nil {
		return false, "coordinator_review_unavailable", nil
	}
	v, err := b.pipeline.RoutineReviewTx(ctx, tx, r.RunID, e.Request.TargetID)
	if err != nil {
		return false, "", err
	}
	if !v.GateOpen || v.Verdict != "ok" || v.Repository != in.Repository || v.HeadSHA != h.HeadSHA {
		return false, "pipeline_review_stale", nil
	}
	now := b.now().UTC()
	var selected *pipelineApproval
	live := false
	for i := len(h.Approvals) - 1; i >= 0; i-- {
		a := &h.Approvals[i]
		if a.HeadSHA != h.HeadSHA {
			continue
		}
		approved, err := approvals.LiveRoutineHoldTx(ctx, tx, p, a.ID, work, "work_orders.write", a.Digest, now)
		if err != nil {
			return false, "", err
		}
		// The decision may arrive between polls, including before revocation.
		var decided bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM approval_decisions WHERE request_id=$1 AND decision='approved')`, a.ID).Scan(&decided); err != nil {
			return false, "", err
		}
		if selected == nil {
			selected = a
		}
		if a.Approved || decided || approved {
			a.Approved = true
			selected = a
			live = approved
			break
		}
	}
	if selected == nil {
		return false, "pipeline_person_approval_or_observation_pending", nil
	}
	var observed string
	err = tx.QueryRow(ctx, `SELECT metadata->>'merge_sha' FROM events WHERE node_id=$1 AND type='delivery.merge_done' AND metadata->>'repository'=$2 AND metadata->>'head_sha'=$3 AND metadata->>'pull_request'=$4 AND metadata->>'merge_sha' ~ '^[a-f0-9]{40}$' AND ($5='' OR metadata->>'merge_sha'=$5) AND ($6='' OR metadata->>'merge_sha'=$6) ORDER BY id DESC LIMIT 1`, work, in.Repository, selected.HeadSHA, fmt.Sprint(pr.PullRequest), selected.QueueHead, merge).Scan(&observed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "merge_done_receipt_pending_or_refused", savePipelineHistoryTx(ctx, tx, r.ID, h)
	}
	if err != nil {
		return false, "", err
	}
	done, reason, err := b.pipeline.RoutineMergeResultTx(ctx, tx, work, in.Repository, selected.HeadSHA, observed, pr.PullRequest)
	if err != nil || !done {
		return false, reason, err
	}
	if !live {
		// An expired decision is visibly renewed against its original digest;
		// only a new live person decision can finish the observation.
		var pending []events.Change
		a, err := approvals.RequestRoutineHoldTx(ctx, tx, p, work, "work_orders.write", selected.Digest, now.Add(24*time.Hour), now, &pending, fmt.Sprintf("Observe merged routine /agents?tab=routines&run=%s; exact PR %s/pull/%d, head %s, merge %s.", r.RunID, in.Repository, pr.PullRequest, selected.HeadSHA, observed))
		if err != nil {
			return false, "", err
		}
		selected.ID = a.ID
		r.ApprovalID, r.State, r.WaitReason = a.ID, "awaiting_decision", "pipeline_person_approval_or_observation_pending"
		if err := savePipelineHistoryTx(ctx, tx, r.ID, h); err != nil {
			return false, "", err
		}
		if err := saveReceiptTx(ctx, tx, *r); err != nil {
			return false, "", err
		}
		for _, change := range pending {
			if _, err := events.Append(ctx, tx, p, change); err != nil {
				return false, "", err
			}
		}
		return false, r.WaitReason, nil
	}
	r.ApprovalID, r.State, r.WaitReason = selected.ID, "succeeded", ""
	if err := savePipelineHistoryTx(ctx, tx, r.ID, h); err != nil {
		return false, "", err
	}
	if err := completePipelineTx(ctx, tx, r.RunID, r.ID); err != nil {
		return false, "", err
	}
	return true, "", saveReceiptTx(ctx, tx, *r)
}

func completePipelineTx(ctx context.Context, tx pgx.Tx, run, id string) error {
	if _, err := tx.Exec(ctx, `UPDATE routine_effect_outbox SET state='succeeded',result=coalesce(result,'{}'::jsonb)||'{"pipeline_complete":true}'::jsonb,updated_at=clock_timestamp() WHERE action_id=$1`, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE routine_runs SET state='completed',updated_at=clock_timestamp() WHERE id=$1 AND state='awaiting_pipeline'`, run)
	return err
}
