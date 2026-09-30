// SPDX-License-Identifier: AGPL-3.0-only

package agentruns

import (
	"context"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Caller holds the pairing, work-order and run locks. Refusal is deliberately
// independent of account readiness: it cannot launch work or grant authority.
func refuseVerification(ctx context.Context, tx pgx.Tx, p tenant.Principal, v Run, daemon, generation, reason string) (any, error) {
	switch reason {
	case "adapter_unsupported", "binding_incomplete", "local_binding_missing":
	default:
		return nil, workorders.Fail(400, "unknown verification refusal")
	}
	if v.Purpose != "pairing_verification" || p.ID != v.AgentID {
		return nil, workorders.Fail(403, "own pairing verification required")
	}
	var owner bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM agent_pairing_enrollments e
 JOIN agent_accounts a ON a.tenant_id=e.tenant_id AND a.id=e.account_id
 JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
 WHERE e.verification_run_id=$1 AND a.registered_by_principal_id=$2
 AND c.principal_id=$2 AND c.daemon_id=$3 AND a.daemon_id=$3
 AND e.account_id=$4 AND ( $5::uuid IS NULL OR e.account_id=$5)
 AND e.verification_claimed_at IS NULL AND e.state='connected' AND c.state='connected')`,
		v.ID, p.ID, daemon, v.RequestedAccountID, v.AccountID).Scan(&owner); err != nil {
		return nil, err
	}
	if !owner {
		return nil, workorders.Fail(403, "own unclaimed enrollment required")
	}
	if v.Status == "failed" && v.VerificationReason == reason && v.StartedAt == nil {
		return v, nil
	}
	if v.Status != "queued" || v.StartedAt != nil || v.DaemonID != nil || v.Generation != nil {
		return nil, workorders.Fail(409, "only unclaimed verification can be refused")
	}
	before := v
	v, err := scan(tx.QueryRow(ctx, `UPDATE agent_runs SET status='failed',ended_at=clock_timestamp(),verification_unavailable_reason=$2 WHERE id=$1 RETURNING `+columns, v.ID, reason))
	if err != nil {
		return nil, err
	}
	if err = agentaccounts.Release(ctx, tx, p, v.ID, daemon, generation); err != nil {
		return nil, err
	}
	return v, workorders.Record(ctx, tx, p, v.OrderID, "run.verification_unavailable", before, v)
}
