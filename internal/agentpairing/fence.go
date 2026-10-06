// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
)

// AccountFence runs with Lock held before account locks. telemetry=true allows
// only already-owned settlement during drain, never after immediate revocation.
func AccountFence(ctx context.Context, tx pgx.Tx, account string, telemetry bool) error {
	var state, computer, request string
	err := tx.QueryRow(ctx, `SELECT e.state,c.state,q.state FROM agent_pairing_enrollments e JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id WHERE e.account_id=$1`, account).Scan(&state, &computer, &request)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state == "revoked" || computer == "revoked" {
		return fail(410, "enrollment_revoked", "enrollment revoked; reconcile local cleanup")
	}
	if request != "redeemed" {
		return fail(409, "conflict", "setup has not been redeemed")
	}
	if !telemetry && (state == "draining" || computer == "draining") {
		return fail(409, "enrollment_draining", "enrollment is draining; no new work")
	}
	return nil
}

// RunFence binds verification to its immutable account, run, expiry and first
// claim. An allowance settling zero does not replenish this authorization.
func RunFence(ctx context.Context, tx pgx.Tx, account, run string, claim bool) error {
	if err := AccountFence(ctx, tx, account, false); err != nil {
		return err
	}
	if err := hostStartFence(ctx, tx, account); err != nil {
		return err
	}
	var verification *string
	var claimed, expired, ongoing bool
	var harness, platform, arch string
	err := tx.QueryRow(ctx, `SELECT q.details->>'platform',q.details->>'arch',(SELECT harness FROM agent_accounts WHERE id=e.account_id),verification_run_id::text,verification_claimed_at IS NOT NULL,e.verification_expires_at<=clock_timestamp(),
  ongoing_approved_at IS NOT NULL
  FROM agent_pairing_enrollments e JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id WHERE account_id=$1`, account).Scan(&platform, &arch, &harness, &verification, &claimed, &expired, &ongoing)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if verification == nil || *verification != run {
		if !ongoing {
			return fail(409, "verification_only", "ongoing account approval required")
		}
		return nil
	}
	if !verificationCapabilities(platform, arch)[harness].Supported {
		return fail(409, "verification_unavailable", "this helper release has no qualified verification mode for the selected harness")
	}
	if expired {
		return fail(409, "verification_expired", "verification expired; setup never replenishes it")
	}
	if claimed {
		return fail(409, "verification_consumed", "verification was already claimed; reconcile its existing run")
	}
	if claim {
		_, err = tx.Exec(ctx, `UPDATE agent_pairing_enrollments SET verification_claimed_at=clock_timestamp() WHERE account_id=$1 AND verification_claimed_at IS NULL`, account)
	}
	return err
}
func PairedPrincipal(ctx context.Context, tx pgx.Tx, principal string) (bool, error) {
	var paired bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_pairing_computers WHERE principal_id=$1)`, principal).Scan(&paired)
	return paired, err
}

// FinishDrain is called only after accepted terminal telemetry has settled its
// existing reservation in the same transaction.
func FinishDrain(ctx context.Context, tx pgx.Tx, account string) error {
	return finishDrain(ctx, tx, account)
}

// FinishDrainDeferred completes enrollment/account/key writes before the outer
// completion appends events. The authenticated telemetry principal remains actor.
func FinishDrainDeferred(ctx context.Context, tx pgx.Tx, account string, pending *[]events.Change) error {
	return finishDrain(ctx, tx, account, pending)
}

func finishDrain(ctx context.Context, tx pgx.Tx, account string, pending ...*[]events.Change) error {
	var computer string
	err := tx.QueryRow(ctx, `SELECT computer_id::text FROM agent_pairing_enrollments WHERE account_id=$1`, account).Scan(&computer)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return finalizeDrain(ctx, tx, computer, pending...)
}
