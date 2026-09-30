// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"encoding/json"
	"math"

	"github.com/jackc/pgx/v5"
)

type EstimateWarning struct {
	Code string `json:"code"`
	Hint string `json:"hint"`
}

type heartbeatResponse struct {
	Session
	Warnings []EstimateWarning `json:"warnings"`
}

// Keep the grace period in the generation, so reporter restarts do not hide a
// missing report. A stored percent does not count as progress on this beat.
func heartbeatEstimateWarnings(ctx context.Context, tx pgx.Tx, s Session, progress json.RawMessage) ([]EstimateWarning, error) {
	// Guidance is optional. Isolate its SQL so a failure cannot leave the
	// heartbeat's transaction aborted after the caller ignores the error.
	warningTx, err := tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = warningTx.Rollback(ctx) }()
	tx = warningTx
	working := s.Phase == "working" && s.StoppedAt == nil && s.ArchivedAt == nil
	_, reported, err := parseProgress(progress)
	if err != nil {
		return nil, err
	}
	missing := working && s.Role == "worker" && (!reported || string(progress) == "null")
	var beats int
	if err := tx.QueryRow(ctx, `UPDATE harness_sessions SET missing_progress_beats=
		CASE WHEN $2 THEN least(missing_progress_beats+1,3) ELSE 0 END
		WHERE id=$1 RETURNING missing_progress_beats`, s.ID, missing).Scan(&beats); err != nil {
		return nil, err
	}
	warnings := make([]EstimateWarning, 0, 3)
	if missing && beats >= 3 {
		warnings = append(warnings, EstimateWarning{"missing_progress", "Report progress_pct (0–100) on each heartbeat; run-heartbeat can read pct from --status-file."})
	}
	if working && s.TicketNodeID != nil && s.Role == "worker" && s.EtaReadyAt == nil {
		warnings = append(warnings, EstimateWarning{"missing_eta", "Report eta_ready_at for the bound ticket; run-heartbeat can read remaining_min from --status-file."})
	} else if working && s.TicketNodeID != nil && s.Role == "coordinator" && s.EtaLiveAt == nil {
		warnings = append(warnings, EstimateWarning{"missing_eta", "Report eta_live_at for the bound ticket (harness heartbeat --eta-live +25m)."})
	}
	if s.TicketNodeID != nil {
		var fields json.RawMessage
		err := tx.QueryRow(ctx, `SELECT n.fields FROM nodes n
			JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
			WHERE n.id=$1 AND n.project_id=$2 AND n.deleted_at IS NULL AND k.slug IN ('ticket','task')`, *s.TicketNodeID, s.ProjectID).Scan(&fields)
		if err != nil && err != pgx.ErrNoRows {
			return nil, err
		}
		if err == nil {
			var doc struct {
				Hours *float64 `json:"estimate_hours"`
			}
			if json.Unmarshal(fields, &doc) != nil || doc.Hours == nil || *doc.Hours <= 0 || *doc.Hours > 200 || math.IsNaN(*doc.Hours) || math.IsInf(*doc.Hours, 0) {
				warnings = append(warnings, EstimateWarning{"ticket_without_estimate", "Set the bound ticket's estimate with issue update KEY --estimate-hours 2 (agent work hours until ready for review)."})
			}
		}
	}
	return warnings, warningTx.Commit(ctx)
}
