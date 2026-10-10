// SPDX-License-Identifier: AGPL-3.0-only
package routinebudget

import (
	"context"
	"errors"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// SubgrantTx carves one qualified provider-call maximum out of the root.
// Settled children consume actual usage; unknown children consume their entire
// maximum. It never increments the run balance or allocates another slot.
func (b Broker) SubgrantTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, parentID string, in Reservation) (Grant, error) {
	if !in.valid() || !workorders.UUID(parentID) {
		return Grant{}, workorders.Fail(400, "invalid_subgrant")
	}
	r, err := begin(ctx, tx, p, in.RunID)
	if err != nil {
		return Grant{}, err
	}
	policy, _, err := b.authorize(ctx, tx, p, r)
	if err != nil {
		return Grant{}, err
	}
	if err := requireRecoveryPolicy(ctx, tx, r.id, policy); err != nil {
		return Grant{}, err
	}
	root, err := scanGrant(tx.QueryRow(ctx, `SELECT `+grantColumns+` FROM routine_budget_grants WHERE id=$1 AND run_id=$2 FOR NO KEY UPDATE`, parentID, in.RunID))
	if err != nil {
		return Grant{}, err
	}
	if root.ParentID != nil || root.State != "held" || root.AttemptID != in.AttemptID || root.ActionID != in.ActionID || root.AgentRunID != in.AgentRunID || root.AccountID != in.AccountID {
		return Grant{}, workorders.Fail(409, "subgrant_parent_unavailable")
	}
	if prior, ok, err := replay(ctx, tx, in, &parentID); err != nil || ok {
		return prior, err
	}
	used, pending, count, err := children(ctx, tx, root.ID)
	if err != nil {
		return Grant{}, err
	}
	if count >= 4096 {
		return Grant{}, workorders.Fail(409, "subgrant_count_exhausted")
	}
	balance, err := loadBalance(ctx, tx, in.RunID)
	if err != nil {
		return Grant{}, err
	}
	current, err := used.add(pending)
	if err != nil {
		return Grant{}, err
	}
	if balance.TokenCeiling != nil && root.Maximum.Tokens > 0 && current.Tokens >= root.Maximum.Tokens {
		return Grant{}, workorders.Fail(409, "token_budget_exhausted")
	}
	if balance.MoneyCeiling != nil && root.Maximum.PaidMicroUSD > 0 && current.PaidMicroUSD >= root.Maximum.PaidMicroUSD {
		return Grant{}, workorders.Fail(409, "money_budget_exhausted")
	}
	total, err := current.add(in.Maximum)
	if err != nil {
		return Grant{}, err
	}
	if !within(total, root.Maximum) {
		return Grant{}, workorders.Fail(409, "subgrant_budget_exhausted")
	}
	return insertGrant(ctx, tx, p, r, in, &parentID, "", root.BillingMode)
}

func children(ctx context.Context, tx pgx.Tx, parent string) (Amount, Amount, int, error) {
	var used, pending Amount
	var count int
	// NUMERIC sum catches overflow before conversion to checked signed int64.
	err := tx.QueryRow(ctx, `SELECT coalesce(sum(used_tokens) FILTER(WHERE state='settled'),0),coalesce(sum(used_microusd) FILTER(WHERE state='settled'),0),coalesce(sum(used_ms) FILTER(WHERE state='settled'),0),
 coalesce(sum(maximum_tokens) FILTER(WHERE state<>'settled'),0),coalesce(sum(maximum_microusd) FILTER(WHERE state<>'settled'),0),coalesce(sum(maximum_ms) FILTER(WHERE state<>'settled'),0),count(*)
 FROM (SELECT * FROM routine_budget_grants WHERE parent_id=$1 LIMIT 4097) bounded`, parent).Scan(&used.Tokens, &used.PaidMicroUSD, &used.RecoveryMS, &pending.Tokens, &pending.PaidMicroUSD, &pending.RecoveryMS, &count)
	if err == nil && count > 4096 {
		err = workorders.Fail(409, "subgrant_count_exhausted")
	}
	return used, pending, count, err
}

// SettleTx accepts only qualified, complete evidence. It can reconcile after
// owner consent is revoked, but still requires the current authenticated run
// reporter or authorized owner; no new grants or external effects are created.
// Unknown receipts are retained as state, not charged as zero or terminalized.
func (b Broker) SettleTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, runID, grantID string, in Settlement) (Grant, error) {
	if !workorders.UUID(grantID) || !key(in.EventKey) || !in.Usage.valid() || !pin(in.EvidenceDigest) {
		return Grant{}, workorders.Fail(400, "invalid_settlement")
	}
	// Personal routine rows are absent from the daemon's ordinary projection.
	// This exact run/grant reconciliation retains tenant RLS and checks current
	// reporter authority under the shared fences before any ledger mutation.
	var visibility, system string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),''),coalesce(current_setting('aeon.system',true),'')`).Scan(&visibility, &system); err != nil {
		return Grant{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`); err != nil {
		return Grant{}, err
	}
	g, err := b.settleTx(ctx, tx, p, runID, grantID, in)
	if err != nil {
		return Grant{}, err // caller must roll back every error
	}
	_, err = tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true),set_config('aeon.system',$2,true)`, visibility, system)
	return g, err
}

func (b Broker) settleTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, runID, grantID string, in Settlement) (Grant, error) {
	r, err := begin(ctx, tx, p, runID)
	if err != nil {
		return Grant{}, err
	}
	g, err := scanGrant(tx.QueryRow(ctx, `SELECT `+grantColumns+` FROM routine_budget_grants WHERE id=$1 AND run_id=$2 FOR NO KEY UPDATE`, grantID, runID))
	if err != nil {
		return Grant{}, err
	}
	var agent string
	if err := tx.QueryRow(ctx, `SELECT agent_principal_id::text FROM agent_runs WHERE id=$1`, g.AgentRunID).Scan(&agent); err != nil {
		return Grant{}, err
	}
	// The assigned daemon reports through the existing run telemetry grant.
	permission := "run.telemetry"
	if p.ID != agent {
		if p.ID != r.owner && p.ID != r.principal {
			return Grant{}, authz.ErrForbidden
		}
		permission = "work_orders.write"
	}
	if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: r.project}); err != nil {
		return Grant{}, err
	}
	digest, err := modelregistry.RoutinePolicyDigest(in)
	if err != nil {
		return Grant{}, err
	}
	var prior string
	err = tx.QueryRow(ctx, `SELECT receipt_digest FROM routine_budget_settlements WHERE grant_id=$1`, g.ID).Scan(&prior)
	if err == nil {
		if prior != digest {
			return Grant{}, workorders.Fail(409, "settlement_replay_conflict")
		}
		return g, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, err
	}
	if !within(in.Usage, g.Maximum) {
		return Grant{}, workorders.Fail(409, "usage_exceeds_grant")
	}
	if g.BillingMode == "subscription" && in.Usage.PaidMicroUSD != 0 {
		return Grant{}, workorders.Fail(409, "subscription_paid_charge_invalid")
	}
	if in.ProvenUnused && (in.Usage != (Amount{}) || !in.UsageComplete || !in.TokensKnown || !in.PaidCostKnown) {
		return Grant{}, workorders.Fail(409, "unused_grant_evidence_conflict")
	}
	// Unknown in either measured dimension keeps the complete hold, even when
	// the routine financial ceiling is off. No invented usage enters history.
	if !in.UsageComplete || !in.TokensKnown || !in.PaidCostKnown || g.ParentID == nil && !in.ExitConfirmed && !in.ProvenUnused {
		if _, err := tx.Exec(ctx, `UPDATE routine_budget_grants SET state='unknown',pending_receipt=$2 WHERE id=$1`, g.ID, in); err != nil {
			return Grant{}, err
		}
		g.State = "unknown"
		return g, nil
	}
	if g.ParentID == nil {
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_budget_grants WHERE parent_id=$1 AND state<>'settled')`, g.ID).Scan(&pending); err != nil {
			return Grant{}, err
		}
		if pending {
			return Grant{}, workorders.Fail(409, "child_settlement_unknown")
		}
		used, _, _, err := children(ctx, tx, g.ID)
		if err != nil {
			return Grant{}, err
		}
		// Tokens and paid charges must reconcile exactly to call receipts. Root
		// recovery time is the actor's measured elapsed time, not call durations.
		if used.Tokens != in.Usage.Tokens || used.PaidMicroUSD != in.Usage.PaidMicroUSD {
			return Grant{}, workorders.Fail(409, "settlement_receipts_incomplete")
		}
		balance, err := loadBalance(ctx, tx, runID)
		if err != nil {
			return Grant{}, err
		}
		balance.Held, err = balance.Held.sub(g.Maximum)
		if err == nil {
			balance.Settled, err = balance.Settled.add(in.Usage)
		}
		if err != nil {
			return Grant{}, err
		}
		if err := writeBalance(ctx, tx, balance); err != nil {
			return Grant{}, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO routine_budget_settlements(tenant_id,run_id,grant_id,event_key,receipt_digest,evidence_digest) VALUES($1,$2,$3,$4,$5,$6)`, p.TenantID, runID, g.ID, in.EventKey, digest, in.EvidenceDigest)
	if err != nil {
		return Grant{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE routine_budget_grants SET state='settled',used_tokens=$2,used_microusd=$3,used_ms=$4,settled_at=clock_timestamp() WHERE id=$1`, g.ID, in.Usage.Tokens, in.Usage.PaidMicroUSD, in.Usage.RecoveryMS)
	g.State, g.Usage = "settled", in.Usage
	return g, err
}
