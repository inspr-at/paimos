// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"net/http"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Lock is acquired before work-order/run/account locks by every paired
// dispatch, claim, probe, settlement and lifecycle mutation. This serializes
// revocation with in-flight credentials that passed the HTTP auth boundary.
func Lock(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-pairing:'||current_setting('aeon.tenant_id'),0))`)
	return err
}

type disconnectInput struct {
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
	Mode             string `json:"mode"`
	AccountID        string `json:"account_id,omitempty"`
}

func (m *Module) disconnect(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var in disconnectInput
	if err := decode(w, r, &in); err != nil {
		WriteError(w, err)
		return
	}
	if in.ExpectedRevision == nil || *in.ExpectedRevision < 1 {
		WriteError(w, fail(400, "invalid_request", "reviewed expected_revision required"))
		return
	}
	if in.AccountID != "" {
		WriteError(w, fail(400, "invalid_request", "use enrollment path to select account"))
		return
	}
	in.AccountID = r.PathValue("accountId")
	var out View
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if authz.RequireTx(r.Context(), tx, p, "account.manage", authz.Scope{}) != nil {
			return fail(403, "forbidden", "person account management required")
		}
		rec, err := computerRecord(r.Context(), tx, r.PathValue("computerId"))
		if err != nil {
			return err
		}
		if err = disconnectTx(r.Context(), tx, p, *rec.ComputerID, in); err != nil {
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
func (m *Module) self(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.Kind != tenant.Agent {
		WriteError(w, fail(403, "forbidden", "paired agent required"))
		return
	}
	var in disconnectInput
	if r.Method == "POST" {
		if err := decode(w, r, &in); err != nil {
			WriteError(w, err)
			return
		}
	}
	var out View
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if authz.RequireTx(r.Context(), tx, p, "run.claim", authz.Scope{}) != nil {
			return fail(403, "forbidden", "runtime permission required")
		}
		var id string
		if err := tx.QueryRow(r.Context(), `SELECT id::text FROM agent_pairing_computers WHERE principal_id=$1 AND state<>'revoked'`, p.ID).Scan(&id); err != nil {
			return notFound(err)
		}
		rec, err := computerRecord(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if r.Method == "POST" {
			if err = disconnectTx(r.Context(), tx, p, id, in); err != nil {
				return err
			}
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
func disconnectTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, computer string, in disconnectInput) error {
	if in.Mode != "drain" && in.Mode != "revoke_now" || in.AccountID != "" && !uuidRE.MatchString(in.AccountID) {
		return fail(400, "invalid_request", "disconnect mode and valid enrollment required")
	}
	var state string
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT state,revision FROM agent_pairing_computers WHERE id=$1 FOR UPDATE`, computer).Scan(&state, &revision); err != nil {
		return notFound(err)
	}
	if in.AccountID != "" {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_pairing_enrollments WHERE computer_id=$1 AND account_id=$2)`, computer, in.AccountID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fail(404, "not_found", "enrollment not found")
		}
	}
	target := "draining"
	if in.Mode == "revoke_now" {
		target = "revoked"
	}
	if in.ExpectedRevision != nil && *in.ExpectedRevision != revision {
		already := state == "revoked" || in.Mode == "drain" && state == "draining"
		if in.AccountID != "" {
			if err := tx.QueryRow(ctx, `SELECT state='revoked' OR ($2='drain' AND state='draining') FROM agent_pairing_enrollments WHERE account_id=$1 AND computer_id=$3`, in.AccountID, in.Mode, computer).Scan(&already); err != nil {
				return err
			}
		}
		if !already {
			return fail(409, "conflict", "computer enrollments changed; review disconnect scope again")
		}
	}
	// Monotonic transitions: a repeated drain cannot undo immediate revocation.
	tag, err := tx.Exec(ctx, `UPDATE agent_pairing_enrollments SET state=$3 WHERE computer_id=$1 AND ($2='' OR account_id=nullif($2,'')::uuid) AND state<>'revoked' AND state<>$3`, computer, in.AccountID, target)
	if err != nil {
		return err
	}
	changed := tag.RowsAffected() > 0
	if in.AccountID == "" && state != "revoked" && state != target {
		if _, err = tx.Exec(ctx, `UPDATE agent_pairing_computers SET state=$2 WHERE id=$1`, computer, target); err != nil {
			return err
		}
		changed = true
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_accounts a SET state=CASE WHEN e.state='revoked' THEN 'unavailable' ELSE 'draining' END
  FROM agent_pairing_enrollments e WHERE e.tenant_id=a.tenant_id AND e.account_id=a.id AND e.computer_id=$1 AND e.state<>'connected'`, computer); err != nil {
		return err
	}
	if err = cancelQueued(ctx, tx, computer, in.AccountID, in.AccountID == ""); err != nil {
		return err
	}
	if in.AccountID == "" && target == "revoked" {
		if err = revokeComputer(ctx, tx, computer); err != nil {
			return err
		}
	}
	if err = finalizeDrain(ctx, tx, computer); err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_pairing_computers SET revision=revision+1 WHERE id=$1`, computer); err != nil {
		return err
	}
	return audit(ctx, tx, p, "agent_pairing.disconnected", map[string]any{"computer_id": computer, "account_id": in.AccountID, "mode": in.Mode, "local_cleanup": "pending", "local_processes": "unconfirmed"})
}

// Queued/reserved work has not launched and can release holds. Active work is
// never changed here, even under immediate revocation or a cleanup assertion.
func cancelQueued(ctx context.Context, tx pgx.Tx, computer, account string, all bool) error {
	rows, err := tx.Query(ctx, `SELECT r.id::text FROM agent_runs r
  WHERE r.status='queued' AND (($3 AND r.agent_principal_id=(SELECT principal_id FROM agent_pairing_computers WHERE id=$1)) OR EXISTS(SELECT 1 FROM agent_pairing_enrollments e WHERE e.computer_id=$1 AND ($2='' OR e.account_id=nullif($2,'')::uuid)
   AND e.state<>'connected' AND (e.account_id=r.account_id OR e.account_id=r.requested_account_id))) ORDER BY r.id FOR UPDATE`, computer, account, all)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.Exec(ctx, `WITH released AS (UPDATE account_reservations SET state='released',settled_at=clock_timestamp() WHERE run_id=$1 AND state='active' RETURNING window_id,reserved_units), totals AS (SELECT window_id,sum(reserved_units)::bigint AS units FROM released GROUP BY window_id)
   UPDATE account_allowance_windows w SET reserved=w.reserved-t.units FROM totals t WHERE w.id=t.window_id`, id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE agent_runs SET status='cancelled',ended_at=clock_timestamp() WHERE id=$1 AND status='queued'`, id); err != nil {
			return err
		}
	}
	return nil
}
func revokeComputer(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, `UPDATE agent_keys SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE principal_id=(SELECT principal_id FROM agent_pairing_computers WHERE id=$1)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_pairing_requests SET state='revoked' WHERE (computer_id=$1 OR details->>'existing_computer_id'=$1::text) AND state IN ('pending','approved','redeemed')`, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE agent_pairing_computers SET state='revoked' WHERE id=$1`, id)
	return err
}
func finalizeDrain(ctx context.Context, tx pgx.Tx, computer string) error {
	tag, err := tx.Exec(ctx, `UPDATE agent_pairing_enrollments e SET state='revoked' WHERE computer_id=$1 AND state='draining'
  AND NOT EXISTS(SELECT 1 FROM agent_runs r WHERE r.account_id=e.account_id AND r.status IN ('starting','running','waiting'))`, computer)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if _, err = tx.Exec(ctx, `UPDATE agent_accounts a SET state='unavailable' FROM agent_pairing_enrollments e WHERE e.tenant_id=a.tenant_id AND e.account_id=a.id AND e.computer_id=$1 AND e.state='revoked'`, computer); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE agent_pairing_computers SET revision=revision+1 WHERE id=$1`, computer); err != nil {
			return err
		}
	}
	var ready bool
	if err = tx.QueryRow(ctx, `SELECT state='draining' AND NOT EXISTS(SELECT 1 FROM agent_pairing_enrollments WHERE computer_id=$1 AND state<>'revoked') FROM agent_pairing_computers WHERE id=$1`, computer).Scan(&ready); err != nil {
		return err
	}
	if ready {
		return revokeComputer(ctx, tx, computer)
	}
	return nil
}
func disconnectRequest(ctx context.Context, tx pgx.Tx, rec record) error {
	if rec.ComputerID == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_pairing_enrollments SET state='revoked' WHERE request_id=$1`, rec.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_accounts a SET state='unavailable' FROM agent_pairing_enrollments e WHERE e.tenant_id=a.tenant_id AND e.account_id=a.id AND e.request_id=$1`, rec.ID); err != nil {
		return err
	}
	if err := cancelQueued(ctx, tx, *rec.ComputerID, "", rec.Details.ExistingComputerID == ""); err != nil {
		return err
	}
	if rec.Details.ExistingComputerID == "" {
		return revokeComputer(ctx, tx, *rec.ComputerID)
	}
	return nil
}
func cleanup(ctx context.Context, tx pgx.Tx, computer string, in proofRequest) error {
	changed := false
	if in.Progress != nil {
		switch in.Progress.State {
		case "provisioning", "login_required", "service_conflict", "connected", "setup_failed":
		default:
			return fail(400, "invalid_request", "unknown setup state")
		}
		switch in.Progress.ErrorCode {
		case "", "login_required", "service_conflict", "unsupported_platform", "managed_installation", "connectivity_failed", "private_storage_failed", "installation_failed":
		default:
			return fail(400, "invalid_request", "unknown setup error")
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_pairing_computers SET setup_state=$2,setup_error=$3,last_seen_at=clock_timestamp() WHERE id=$1 AND state='connected'`, computer, in.Progress.State, in.Progress.ErrorCode); err != nil {
			return err
		}
	}

	for _, id := range in.Cleaned {
		if !uuidRE.MatchString(id) {
			return fail(400, "invalid_request", "invalid cleanup enrollment")
		}
		var previous string
		if err := tx.QueryRow(ctx, `SELECT local_cleanup FROM agent_pairing_enrollments WHERE computer_id=$1 AND account_id=$2 AND state='revoked'`, computer, id).Scan(&previous); err != nil {
			return fail(409, "conflict", "only a revoked own enrollment can acknowledge cleanup")
		}
		changed = changed || previous != "confirmed"
		tag, err := tx.Exec(ctx, `UPDATE agent_pairing_enrollments SET local_cleanup='confirmed' WHERE computer_id=$1 AND account_id=$2 AND state='revoked'`, computer, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fail(409, "conflict", "only a revoked own enrollment can acknowledge cleanup")
		}
	}
	if in.ComputerCleaned {
		var previous string
		if err := tx.QueryRow(ctx, `SELECT local_cleanup FROM agent_pairing_computers WHERE id=$1`, computer).Scan(&previous); err != nil {
			return err
		}
		changed = changed || previous != "confirmed"
		tag, err := tx.Exec(ctx, `UPDATE agent_pairing_computers SET local_cleanup='confirmed',local_processes='drained' WHERE id=$1 AND state='revoked' AND NOT EXISTS(SELECT 1 FROM agent_pairing_enrollments WHERE computer_id=$1 AND (state<>'revoked' OR local_cleanup<>'confirmed'))`, computer)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fail(409, "conflict", "all revoked enrollments must acknowledge cleanup first")
		}
	}
	// Cleanup proof is an assertion from the device, not usage or run telemetry:
	// outstanding active runs/reservations remain unconfirmed and need person
	// recovery. A client cannot erase accounting through this public endpoint.
	if changed {
		var principal, tenantID string
		if err := tx.QueryRow(ctx, `SELECT principal_id::text,tenant_id::text FROM agent_pairing_computers WHERE id=$1`, computer).Scan(&principal, &tenantID); err != nil {
			return err
		}
		return audit(ctx, tx, tenant.Principal{ID: principal, TenantID: tenantID, Kind: tenant.Agent}, "agent_pairing.cleanup_acknowledged", map[string]any{"computer_id": computer, "account_ids": in.Cleaned, "computer_cleanup_confirmed": in.ComputerCleaned})
	}
	return nil
}
