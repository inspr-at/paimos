// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// remove archives a revoked or never-confirmed computer (AEON-402). It leaves
// the computers list with its account bindings; the rows, runs and audit
// events stay. A never-confirmed computer is revoked first, in the same step.
// Unsettled runs keep the computer in the list until their accounting is known.
func (m *Module) remove(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var out View
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if authz.RequireTx(r.Context(), tx, p, "account.manage", authz.Scope{}) != nil {
			return fail(403, "forbidden", "person account management required")
		}
		rec, err := computerRecord(r.Context(), tx, r.PathValue("computerId"))
		if err != nil {
			return err
		}
		if err = removeComputerTx(r.Context(), tx, p, *rec.ComputerID); err != nil {
			return err
		}
		out, err = view(r.Context(), tx, rec, false)
		return err
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}

func removeComputerTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, computer string) error {
	var state, request string
	var archived *time.Time
	var seen *time.Time
	if err := tx.QueryRow(ctx, `SELECT c.state,c.archived_at,c.last_seen_at,q.state FROM agent_pairing_computers c
  JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id WHERE c.id=$1 FOR UPDATE OF c`, computer).Scan(&state, &archived, &seen, &request); err != nil {
		return notFound(err)
	}
	if archived != nil {
		return nil
	}
	if state != "revoked" {
		// Never confirmed: approved, but the computer never redeemed or reported.
		if seen != nil || request == "redeemed" {
			return fail(409, "computer_connected", "disconnect this computer before removing it")
		}
		if err := disconnectTx(ctx, tx, p, computer, disconnectInput{Mode: "revoke_now"}); err != nil {
			return err
		}
	}
	var unsettled bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs r JOIN agent_pairing_enrollments e ON e.tenant_id=r.tenant_id AND e.account_id=r.account_id
  WHERE e.computer_id=$1 AND (r.status IN ('starting','running','waiting')
   OR r.trace->'work_lifecycle_release'->>'exit_unconfirmed'='true'))`, computer).Scan(&unsettled); err != nil {
		return err
	}
	if unsettled {
		return fail(409, "runs_unsettled", "runs on this computer are not settled yet")
	}
	var accounts []string
	rows, err := tx.Query(ctx, `UPDATE agent_accounts a SET archived_at=clock_timestamp(),archived_by_principal_id=$2,state='unavailable'
  FROM agent_pairing_enrollments e WHERE e.tenant_id=a.tenant_id AND e.account_id=a.id AND e.computer_id=$1 AND a.archived_at IS NULL RETURNING a.id::text`, computer, p.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		accounts = append(accounts, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_pairing_computers SET archived_at=clock_timestamp(),archived_by_principal_id=$2,revision=revision+1 WHERE id=$1`, computer, p.ID); err != nil {
		return err
	}
	return audit(ctx, tx, p, "agent_pairing.removed", map[string]any{"computer_id": computer, "account_ids": accounts, "was": state})
}

// ErrActiveRuns reports that an account binding still has work in progress.
var ErrActiveRuns = errors.New("account has active runs")

// DisconnectAccount ends a paired account binding before a person removes the
// account (AEON-402): queued runs are cancelled and the enrollment is revoked.
// It refuses while a run is starting, running or waiting on the account, so
// accounting stays known. An unpaired account, or a revoked one, is a no-op.
func DisconnectAccount(ctx context.Context, tx pgx.Tx, p tenant.Principal, account string) error {
	var computer, state string
	err := tx.QueryRow(ctx, `SELECT computer_id::text,state FROM agent_pairing_enrollments WHERE account_id=$1`, account).Scan(&computer, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE account_id=$1 AND (status IN ('starting','running','waiting')
  OR trace->'work_lifecycle_release'->>'exit_unconfirmed'='true'))`, account).Scan(&active); err != nil {
		return err
	}
	if active {
		return ErrActiveRuns
	}
	if state == "revoked" {
		return nil
	}
	// With no active run, a drain ends at once: finalizeDrain revokes it.
	return disconnectTx(ctx, tx, p, computer, disconnectInput{Mode: "drain", AccountID: account})
}
