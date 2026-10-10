// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"encoding/json"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const RoutinePickupHeader = "X-Aeon-Routine-Pickup"

// RequireRoutineRouteTx refuses old daemons before account reservation, using
// the immutable server trace rather than caller-provided run/prompt metadata.
// The final claim independently rechecks live qualification/admission/budget.
func RequireRoutineRouteTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, run, daemon string, launch *string, trace []byte, pickupHeader, ledger string) error {
	var saved struct {
		RunID string                   `json:"routine_run_id"`
		Grant *agentsetup.RoutineGrant `json:"routine_launch"`
	}
	if len(trace) > 262144 {
		return fail(409, "routine_binding_unavailable", "routine trace unavailable")
	}
	if len(trace) > 0 && json.Unmarshal(trace, &saved) != nil {
		return fail(409, "routine_binding_unavailable", "routine trace unavailable")
	}
	if saved.RunID == "" && saved.Grant == nil && launch == nil {
		return nil
	}
	g := saved.Grant
	if g == nil || launch == nil || *launch != g.LaunchID || g.AgentRunID != run || g.Validate() != nil {
		return fail(409, "routine_intent_required", "qualified routine intent required before routing")
	}
	var pickup agentsetup.RoutinePickup
	if len(pickupHeader) > 2048 || json.Unmarshal([]byte(pickupHeader), &pickup) != nil || pickup.LaunchID != g.LaunchID || pickup.Capability != g.Capability || pickup.DaemonDigest != g.DaemonDigest || pickup.CapabilityDigest != g.CapabilityDigest || pickup.ComputerID != g.ComputerID || !agentsetup.RoutineDigest(pickup.WorktreeID) {
		return fail(409, "routine_daemon_update_required", "qualified routine daemon required before routing")
	}
	if g.TenantID != p.TenantID || g.PrincipalID != p.ID || g.DaemonID != daemon || g.LedgerGeneration != ledger {
		return fail(409, "routine_daemon_binding_changed", "routine daemon binding changed")
	}
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_routine_execution_settings s JOIN routine_execution_qualifications q ON q.tenant_id=s.tenant_id AND q.id=s.qualification_id
 WHERE s.project_id=$1 AND s.automatic_launch_enabled AND s.revision=$2 AND q.id=$3 AND q.revoked_at IS NULL
 AND q.daemon_digest=$4 AND q.capability_digest=$5 AND q.host_mapping_digest=$6 AND clock_timestamp()<$7)`, g.ProjectID, g.ExecutionRevision, g.QualificationID, g.DaemonDigest, g.CapabilityDigest, g.HostMappingDigest, g.ExpiresAt).Scan(&enabled)
	if err != nil {
		return err
	}
	if !enabled {
		return fail(409, "routine_execution_unavailable", "routine execution consent/qualification unavailable")
	}
	return RequireRoutineAccountTx(ctx, tx, *g)
}

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
