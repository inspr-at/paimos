// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"errors"
	"net/http"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// confirmExit is the sole worker exception to the archived-generation fence.
// The executor can report a later confirmed exit, but cannot service, revive,
// rebind or otherwise mutate that generation. Archive receipts keep the facts
// observed at removal; stop_confirmed separately audits the later exit report.
func (m *Module) confirmExit(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Reason string `json:"reason"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	switch in.Reason {
	case "process_exited", "process_failed", "force_stopped":
	default:
		return nil, workorders.Fail(400, "confirmed process exit reason required")
	}
	ctx := r.Context()
	// Endpoint holds the work-tree/access fence before this check and row lock.
	if err := authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: r.PathValue("projectId")}); err != nil {
		return nil, err
	}
	s, err := load(ctx, tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, workorders.Fail(403, "harness worker proof rejected")
	}
	if err != nil {
		return nil, err
	}
	if !leaseProof(s, r, p) {
		return nil, workorders.Fail(403, "harness worker proof rejected")
	}
	if s.StoppedAt == nil {
		return nil, workorders.Fail(409, "stopped generation required for exit confirmation")
	}
	var confirmed bool
	if err := tx.QueryRow(ctx, `SELECT aeon_work_session_stopped(stopped_at,stop_reason) FROM harness_sessions WHERE id=$1`, s.ID).Scan(&confirmed); err != nil {
		return nil, err
	}
	if confirmed {
		if s.StopReason != nil && *s.StopReason == in.Reason {
			return s, nil
		}
		return nil, workorders.Fail(409, "divergent exit confirmation")
	}
	before := s
	s, err = scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET stop_reason=$2,revision=revision+1 WHERE id=$1 RETURNING `+sessionColumns, s.ID, in.Reason))
	if err != nil {
		return nil, err
	}
	return s, record(ctx, tx, p, s, "stop_confirmed", before, s)
}
