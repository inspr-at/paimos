// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// escalationForWork makes existing ticket-aware routing consume tier A. It
// changes only the next build plan; managed starts still prove exit and reserve
// through the shared retry adapter. Review routes retain their exact gate.
func escalationForWork(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, now time.Time) (*WorkResolution, error) {
	if q.TicketID == "" || q.Role != "build" && q.Role != "build-hard" {
		return nil, nil
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT state FROM work_escalations WHERE ticket_node_id=$1`, q.TicketID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state struct {
		Status       string   `json:"status"`
		UsedProfiles []string `json:"used_profile_ids"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	if state.Status != "stuck" && state.Status != "awaiting_decision" {
		return nil, nil
	}
	if state.Status == "awaiting_decision" {
		return &WorkResolution{Resolution: Resolution{Role: q.Role, Source: "aeon", OwnerRequired: true, Ladder: []Candidate{}}, Trace: PreferenceTrace{Blocked: "stuck work awaits a Decision Desk decision"}}, nil
	}
	var current *string
	err = tx.QueryRow(ctx, `SELECT (SELECT model_profile_id::text FROM harness_sessions WHERE ticket_node_id=$1 AND role='worker' AND model_profile_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM work_orders w WHERE w.node_id=harness_sessions.work_order_id AND w.kind='review') ORDER BY created_at DESC,id DESC LIMIT 1)`, q.TicketID).Scan(&current)
	if err != nil {
		return nil, err
	}
	excluded := state.UsedProfiles
	if current != nil {
		exists := false
		for _, id := range excluded {
			if id == *current {
				exists = true
			}
		}
		if !exists {
			excluded = append(excluded, *current)
		}
	}
	out, err := ResolveEscalation(ctx, tx, p, q, excluded, now)
	return &out, err
}
