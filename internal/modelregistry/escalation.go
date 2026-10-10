// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/modelprefs"
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
	if len(excluded) > 0 && q.PreviousFamily == "" {
		if err := tx.QueryRow(ctx, `SELECT family FROM model_profiles WHERE id=$1`, excluded[0]).Scan(&q.PreviousFamily); err != nil {
			return out, err
		}
	}
	if board, err := resolveBoardWork(ctx, tx, p, q, now, excluded); err != nil {
		return out, err
	} else if board != nil {
		return *board, nil
	}
	// Bound fan-out before decoding profiles or projecting account windows.
	var tooLarge bool
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM (SELECT 1 FROM model_profiles LIMIT 513) p)>512 OR (SELECT count(*) FROM (SELECT 1 FROM agent_accounts LIMIT 257) a)>256 OR (SELECT count(*) FROM (SELECT 1 FROM model_role_routes WHERE role='build-hard' LIMIT 65) r)>64 OR (SELECT count(*) FROM (SELECT 1 FROM account_allowance_windows WHERE NOT pairing_verification AND NOT capacity_retired AND removed_at IS NULL LIMIT 4097) w)>4096`).Scan(&tooLarge); err != nil {
		return out, err
	}
	if tooLarge {
		out.Trace.Blocked = "escalation registry limit exceeded"
		return out, nil
	}
	if q.PersonID == nil {
		q.PersonID = modelprefs.PrefsPerson(ctx, tx, p)
	}
	chain, trace, requirement, err := placementTrace(ctx, tx, q)
	if err != nil {
		return out, err
	}
	out.Trace, out.Residency = trace, requirement
	kind, fallback, err := modelprefs.LookupKind(ctx, tx, q.Area, q.ProjectID)
	if err != nil {
		return out, err
	}
	out.Trace.Kind = kind.Slug
	out.Trace.KindSource = "ticket"
	if fallback {
		out.Trace.KindSource = "fallback"
	}
	cell := modelprefs.ResolveCell(chain, kind.Slug, out.Trace.Bucket)
	traceCell(&out.Trace, cell)
	// Locked preferences may select an allowed stronger route, but cannot
	// silently fall through to another model outside their selector.
	locked := cell.Cell != nil && cell.Cell.Mode != "auto" && cell.LockedBy != ""
	steps, err := loadLadder(ctx, tx, "build-hard")
	if err != nil {
		return out, err
	}
	// Fable/Astra must be explicitly allowed for build-hard in the registry;
	// their presence on the review ladder alone does not grant build authority.
	preferred := map[string]int{}
	if cell.Cell != nil && cell.Cell.Mode != "auto" {
		profiles, err := listProfiles(ctx, tx)
		if err != nil {
			return out, err
		}
		for i, pr := range expandPreference(*cell.Cell, profiles) {
			preferred[pr.ID] = i + 1
		}
	}
	sort.SliceStable(steps, func(i, j int) bool {
		a, b := steps[i].Profile, steps[j].Profile
		if preferred[a.ID] != preferred[b.ID] {
			if preferred[a.ID] == 0 {
				return false
			}
			if preferred[b.ID] == 0 {
				return true
			}
			return preferred[a.ID] < preferred[b.ID]
		}
		return escalationRank(a, q.Area) < escalationRank(b, q.Area)
	})
	identities := map[string]bool{}
	for _, id := range excluded {
		var harness, model, effort string
		if err := tx.QueryRow(ctx, `SELECT harness,model,effort FROM model_profiles WHERE id=$1`, id).Scan(&harness, &model, &effort); err != nil {
			return out, err
		}
		identities[harness+"/"+model+"/"+effort] = true
	}
	waiting := false
	health, err := agentaccounts.HarnessHealthAt(ctx, tx, now)
	if err != nil {
		return out, err
	}
	role, _ := roleByName("build-hard")
	for _, step := range steps {
		pr := step.Profile
		reasons := skipReasons(step, role, resolveQuery{Role: "build-hard", Harness: q.Harness, OffHarnesses: q.OffHarnesses}, now, health)
		if locked && preferred[pr.ID] == 0 {
			reasons = append(reasons, "locked model preference")
		}
		if escalationRank(pr, q.Area) >= 99 {
			reasons = append(reasons, "not a stronger route for this work area")
		}
		if identities[pr.Harness+"/"+pr.Model+"/"+pr.Effort] {
			reasons = append(reasons, "already attempted")
		}
		policyReasons := skipReasons(step, role, resolveQuery{Role: "build-hard", Harness: q.Harness, OffHarnesses: q.OffHarnesses}, now, nil)
		if len(policyReasons) == 0 && escalationRank(pr, q.Area) < 99 && !identities[pr.Harness+"/"+pr.Model+"/"+pr.Effort] && (!locked || preferred[pr.ID] > 0) {
			waiting = true
		}
		ids, err := agentaccounts.QualifyingAccountIDs(ctx, tx, pr.ID, pr.Harness, q.ProjectID, requirement, now)
		if err != nil {
			return out, err
		}
		ids, err = agentaccounts.EscalationRoomIDs(ctx, tx, ids, now)
		if err != nil {
			return out, err
		}
		if len(ids) == 0 {
			reasons = append(reasons, "no qualified account; wait for live admission")
		}
		c := Candidate{ProfileID: pr.ID, SkipReasons: reasons}
		if len(reasons) == 0 && out.Profile == nil {
			out.Profile = &pr
			out.OwnerRequired = false
			c.Selected = true
			out.Trace.QualifyingAccountIDs = ids
			out.CommandTemplate, err = commandTemplate(pr.Harness, pr.Model, pr.Effort, false)
			if err != nil {
				return out, err
			}
		}
		out.Ladder = append(out.Ladder, c)
	}
	if out.Profile == nil {
		out.Trace.Blocked = "no allowed stronger route"
		if locked {
			out.Trace.Blocked = "locked model preference"
		}
		if waiting {
			out.Trace.Blocked = "admission_wait"
			out.OwnerRequired = false
		}
	}
	return out, nil
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
