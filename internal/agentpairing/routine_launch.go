// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/jackc/pgx/v5"
)

// RequireRoutineAccountTx requires the owner's registered native Mac, exact
// computer/account/daemon enrollment and ongoing consent. Manual unpaired
// accounts remain compatible through the existing AccountFence.
// The caller holds tenant/tree/pairing fences through its final write.
func RequireRoutineAccountTx(ctx context.Context, tx pgx.Tx, g agentsetup.RoutineGrant) error {
	var matches bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_accounts a
 JOIN agent_pairing_enrollments e ON e.tenant_id=a.tenant_id AND e.account_id=a.id
 JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
 JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id
 WHERE a.id=$1 AND c.id=$2 AND q.approved_by=$3 AND c.principal_id=$4
 AND a.registered_by_principal_id=$4 AND a.daemon_id=$5 AND a.last_daemon_generation=$6
 AND c.ledger_generation=$7::uuid AND c.state='connected' AND e.state='connected' AND q.state='redeemed'
 AND e.ongoing_approved_at IS NOT NULL AND q.details->>'platform'='darwin'
 AND a.last_probe_ok AND a.last_probe_at>clock_timestamp()-interval '2 minutes')`, g.AccountID, g.ComputerID, g.OwnerID, g.PrincipalID, g.DaemonID, g.DaemonGeneration, g.LedgerGeneration).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return fail(409, "routine_host_account_unavailable", "qualified owner workstation enrollment required")
	}
	return AccountFence(ctx, tx, g.AccountID, false)
}
