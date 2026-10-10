// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// ResolveEscalation selects a stronger build route, never a reviewer. Registry
// pins, retirement, preference locks, harness health and qualified accounts
// remain authoritative. This is planning, not launch admission.
func ResolveEscalation(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, excluded []string, now time.Time) (WorkResolution, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out := WorkResolution{Resolution: Resolution{Role: "build-hard", Source: "aeon", Ladder: []Candidate{}, OwnerRequired: true}}
	if len(excluded) > 3 {
		return out, fail(400, "too many escalation exclusions")
	}
	if q.Role != "build" && q.Role != "build-hard" {
		out.Trace.Blocked = "role has no automatic escalation"
		return out, nil
	}
	q.Situation = "stuck"
	q.Escalation = true
	if len(excluded) > 0 && q.PreviousFamily == "" {
		if err := tx.QueryRow(ctx, `SELECT family FROM model_profiles WHERE id=$1`, excluded[0]).Scan(&q.PreviousFamily); err != nil {
			return out, err
		}
	}
	q.Role = "build-hard"
	board, err := resolveBoardWork(ctx, tx, p, q, now, excluded)
	if err != nil {
		return out, err
	}
	return *board, nil
}

func escalationRank(p Profile, area string) int {
	_, line, _ := ProfileLine(p)
	if p.Family == "anthropic" && p.Harness == "claude" {
		if line == "opus" && p.Effort == "xhigh" {
			return 1
		}
		if !uiEscalationArea(area) && line == "fable" && p.Effort == "xhigh" {
			return 0
		}
	}
	if !uiEscalationArea(area) && p.Harness == "codex" && p.Family == "openai" && line == "astra" && p.Effort == "xhigh" {
		return 2
	}
	return 99
}

func uiEscalationArea(area string) bool {
	return area == "ui" || area == "frontend" || area == "design"
}
