// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"math"

	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/jackc/pgx/v5"
)

func boardTicketFields(ctx context.Context, tx pgx.Tx, q WorkQuery, raw []byte) (WorkQuery, error) {
	if len(raw) > 1<<20 {
		return q, prefFail(413, "ticket_fields_limit")
	}
	var fields struct {
		EstimateHours float64         `json:"estimate_hours"`
		FixRound      int             `json:"fix_round"`
		Labels        []string        `json:"labels"`
		Assignee      json.RawMessage `json:"assignee"`
		AssigneeID    string          `json:"assignee_id"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return q, err
	}
	if fields.EstimateHours < 0 || math.IsNaN(fields.EstimateHours) || math.IsInf(fields.EstimateHours, 0) || fields.FixRound < 0 || fields.FixRound > 1000 || len(fields.Labels) > 64 {
		return q, prefFail(422, "invalid_board_placement")
	}
	for _, label := range fields.Labels {
		if len(label) > 48 {
			return q, prefFail(422, "invalid_board_placement")
		}
	}
	q.EstimateHours, q.FixRound, q.Labels = fields.EstimateHours, fields.FixRound, fields.Labels
	if q.TicketID != "" && q.FixRound == 0 {
		var round *int
		if err := tx.QueryRow(ctx, `SELECT (SELECT (state->>'fix_rounds')::int FROM work_escalations WHERE ticket_node_id=$1)`, q.TicketID).Scan(&round); err != nil {
			return q, err
		}
		if round != nil {
			q.FixRound = *round
		}
	}
	if q.Queued {
		id := fields.AssigneeID
		if len(fields.Assignee) > 0 {
			var assignee string
			if json.Unmarshal(fields.Assignee, &assignee) == nil {
				id = assignee
			} else {
				var a struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(fields.Assignee, &a); err == nil {
					id = a.ID
				}
			}
		}
		if uuidRE.MatchString(id) {
			person, err := modelprefs.CanonicalPerson(ctx, tx, id)
			if err != nil {
				return q, err
			}
			if person != nil {
				q.PersonID = person
			}
		}
	}
	return q, nil
}
