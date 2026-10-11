// SPDX-License-Identifier: AGPL-3.0-only
package routineactions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/routineactions/evidence"
	"github.com/inspr-at/paimos/internal/routineguard"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type PROpen = evidence.PROpen

// PullRequests is deliberately limited to one approved App operation. There
// is no merge, queue, publication or deployment method. Find must return a
// complete bounded same-repository inventory or an error, never partial absence.
type PullRequests interface {
	Configured(string, string) bool
	Find(context.Context, PROpen, string) (*Pull, error)
	Open(context.Context, PROpen, string) (*Pull, error)
}

type Pull = evidence.Pull

func (b *Broker) WithPullRequests(pr PullRequests) *Broker { b.pr = pr; return b }
func (b *Broker) WithPipeline(p Pipeline) *Broker          { b.pipeline = p; return b }

type effect struct {
	Request      Request `json:"request"`
	PrincipalID  string  `json:"principal_id"`
	OperationRef string  `json:"operation_ref"`
}

func prepareEffectTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, binding Binding, in Request, policy, evaluation string) (Receipt, error) {
	digest := routineguard.Digest(in)
	var priorDigest string
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT request_digest,result FROM routine_actions WHERE run_id=$1 AND action_key=$2 FOR NO KEY UPDATE`, binding.RunID, in.ActionKey).Scan(&priorDigest, &raw)
	if err == nil {
		if priorDigest != digest {
			return Receipt{}, workorders.Fail(409, "action_key_conflict")
		}
		var out Receipt
		if json.Unmarshal(raw, &out) != nil || out.RequestDigest != digest {
			return Receipt{}, workorders.Fail(409, "action_receipt_invalid")
		}
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, err
	}
	if err := requireTarget(ctx, tx, binding, in); err != nil {
		return Receipt{}, err
	}
	var work string
	if err := tx.QueryRow(ctx, `SELECT work_node_id::text FROM routine_runs WHERE id=$1`, binding.RunID).Scan(&work); err != nil {
		return Receipt{}, err
	}
	if work != in.TargetID {
		return Receipt{}, workorders.Fail(409, "external_action_target_changed")
	}
	if in.PipelineRequest != nil {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_actions WHERE run_id=$1 AND kind='pipeline.request')`, binding.RunID).Scan(&exists); err != nil {
			return Receipt{}, err
		}
		if exists {
			return Receipt{}, workorders.Fail(409, "pipeline_request_exists")
		}
	}
	var allowed struct {
		AllowedActions []string `json:"allowed_actions"`
	}
	if json.Unmarshal(binding.Assignment, &allowed) != nil || !slices.Contains(allowed.AllowedActions, in.Kind) {
		return Receipt{}, authz.ErrForbidden
	}
	out := Receipt{RunID: binding.RunID, ActionKey: in.ActionKey, Kind: in.Kind, RequestDigest: digest, State: "pending", NodeID: in.TargetID, Revision: in.ExpectedRevision}
	out.OperationRef = routineguard.Digest([]string{p.TenantID, binding.RunID, in.ActionKey, digest})
	if err := tx.QueryRow(ctx, `INSERT INTO routine_actions(tenant_id,run_id,attempt_id,action_key,kind,request_digest,target_node_id,target_revision,policy_digest,evaluation_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id::text`, p.TenantID, binding.RunID, in.AttemptID, in.ActionKey, in.Kind, digest, in.TargetID, in.ExpectedRevision, policy, evaluation).Scan(&out.ID); err != nil {
		return Receipt{}, err
	}
	payload, _ := json.Marshal(effect{in, p.ID, out.OperationRef})
	if _, err := tx.Exec(ctx, `INSERT INTO routine_effect_outbox(tenant_id,run_id,action_id,effect_key,kind,payload) VALUES($1,$2,$3,$4,$5,$6)`, p.TenantID, binding.RunID, out.ID, "action:"+out.ID, in.Kind, payload); err != nil {
		return Receipt{}, err
	}
	return out, saveReceiptTx(ctx, tx, out)
}

func saveReceiptTx(ctx context.Context, tx pgx.Tx, out Receipt) error {
	raw, _ := json.Marshal(out)
	_, err := tx.Exec(ctx, `UPDATE routine_actions SET state=$2,result=$3,approval_id=nullif($4,'')::uuid,updated_at=clock_timestamp() WHERE id=$1`, out.ID, out.State, raw, out.ApprovalID)
	return err
}

// effectTx is the final-write authority fence for both reconciliation and the
// durable send claim. Caller permissions never come from a saved API key.
func (b *Broker) effectTx(ctx context.Context, tid, id string, fn func(pgx.Tx, tenant.Principal, Binding, effect, string, *Receipt) error) error {
	return db.InTenant(db.AllProjects(ctx, "routine catalogue effect reconciliation"), b.pool, tid, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		var run, state string
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT run_id::text,state,payload FROM routine_effect_outbox WHERE action_id=$1 AND kind IN ('pr.open','pipeline.request') FOR NO KEY UPDATE`, id).Scan(&run, &state, &raw); err != nil {
			return err
		}
		var v effect
		if len(raw) > MaxBodyBytes || json.Unmarshal(raw, &v) != nil || !workorders.UUID(v.PrincipalID) || v.Request.Validate() != nil {
			return workorders.Fail(409, "effect_receipt_invalid")
		}
		p := tenant.Principal{ID: v.PrincipalID, TenantID: tid}
		var kind string
		if err := tx.QueryRow(ctx, `SELECT kind FROM principals WHERE id=$1 AND status='active' AND linked_to IS NULL`, p.ID).Scan(&kind); err != nil {
			return err
		}
		p.Kind = tenant.PrincipalKind(kind)
		binding, actor, decision, evaluation, err := b.authorizedTx(ctx, tx, p, run, v.Request, true)
		if err != nil {
			return err
		}
		if err := requireTarget(ctx, tx, binding, v.Request); err != nil {
			return err
		}
		var result []byte
		if err := tx.QueryRow(ctx, `SELECT result FROM routine_actions WHERE id=$1 AND run_id=$2 FOR NO KEY UPDATE`, id, run).Scan(&result); err != nil {
			return err
		}
		var out Receipt
		if json.Unmarshal(result, &out) != nil || out.ID != id || out.RequestDigest != routineguard.Digest(v.Request) || out.OperationRef != v.OperationRef || v.OperationRef != routineguard.Digest([]string{tid, run, v.Request.ActionKey, out.RequestDigest}) {
			return workorders.Fail(409, "effect_binding_changed")
		}
		if _, err := tx.Exec(ctx, `UPDATE routine_actions SET policy_digest=$2,evaluation_id=$3 WHERE id=$1`, id, decision.PolicyDigest, evaluation); err != nil {
			return err
		}
		if err := fn(tx, actor, binding, v, state, &out); err != nil {
			return err
		}
		return saveReceiptTx(ctx, tx, out)
	})
}

// ReconcilePR permits at most one POST per durable reference. A crash between
// the committed send claim and POST leaves uncertainty for a person to resolve;
// it does not manufacture absence or retry an irreversible effect.
func (b *Broker) ReconcilePR(ctx context.Context, tid, id string) (Receipt, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var v effect
	var out Receipt
	var state string
	if err := b.effectTx(ctx, tid, id, func(_ pgx.Tx, _ tenant.Principal, _ Binding, e effect, s string, r *Receipt) error {
		v, state, out = e, s, *r
		return nil
	}); err != nil {
		return out, err
	}
	if v.Request.PROpen == nil {
		return out, workorders.Fail(409, "pr_action_required")
	}
	if out.State == "succeeded" {
		return out, nil
	}
	ref := v.OperationRef
	in := *v.Request.PROpen
	reason := "github_app_unavailable"
	var pull *Pull
	if b.pr != nil && b.pr.Configured(tid, in.Repository) {
		var err error
		pull, err = b.pr.Find(ctx, in, ref)
		reason = "pr_reconciliation_unconfirmed"
		if err == nil && pull == nil && state == "pending" {
			claimed := false
			err = b.effectTx(ctx, tid, id, func(tx pgx.Tx, _ tenant.Principal, _ Binding, _ effect, s string, r *Receipt) error {
				if s != "pending" || r.State == "succeeded" {
					return nil
				}
				_, err := tx.Exec(ctx, `UPDATE routine_effect_outbox SET state='sending',updated_at=clock_timestamp() WHERE action_id=$1`, id)
				claimed = err == nil
				return err
			})
			if err != nil {
				return out, err
			}
			if claimed {
				pull, err = b.pr.Open(ctx, in, ref)
			}
		}
		if err != nil {
			pull = nil
		}
		if pull != nil && !pull.Matches(in, ref) {
			reason = "pr_binding_changed"
			pull = nil
		}
	}
	err := b.effectTx(ctx, tid, id, func(tx pgx.Tx, _ tenant.Principal, _ Binding, _ effect, _ string, r *Receipt) error {
		if r.State == "succeeded" {
			out = *r
			return nil
		}
		r.WaitReason = reason
		if pull != nil {
			r.State, r.WaitReason, r.PullRequest = "succeeded", "", pull.Number
			r.PullURL = fmt.Sprintf("https://github.com/%s/pull/%d", in.Repository, pull.Number)
			if _, err := tx.Exec(ctx, `UPDATE routine_effect_outbox SET state='succeeded',result=$2,updated_at=clock_timestamp() WHERE action_id=$1`, id, []byte(`{"confirmed":true}`)); err != nil {
				return err
			}
		}
		out = *r
		return nil
	})
	return out, err
}
