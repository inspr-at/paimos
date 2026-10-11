// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// resolveDailyWork uses the existing ranked order and qualification, excluding
// exhausted harnesses only after the selected harness explicitly permits it.
// A pinned harness, unknown reading or wait setting never becomes a fallback.
func resolveDailyWork(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, now time.Time) (WorkResolution, error) {
	return resolveDailyWith(ctx, tx, p, q, now, func(query WorkQuery) (WorkResolution, error) {
		return resolveWorkWithCatalog(ctx, tx, p, query, now, nil)
	})
}

func resolveDailyWith(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, now time.Time, resolve func(WorkQuery) (WorkResolution, error)) (WorkResolution, error) {
	out, err := resolve(q)
	if err != nil {
		return out, err
	}
	person := q.PersonID
	if out.Trace.PersonID != nil {
		person = out.Trace.PersonID
	}
	if person == nil {
		person = modelprefs.PrefsPerson(ctx, tx, p)
	}
	if person == nil {
		return out, nil
	} // workspace/catalog preview, no launch authority
	input, err := tx.Begin(ctx)
	if err != nil {
		return out, err
	}
	tenantID := p.TenantID
	if tenantID == "" {
		if err := input.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')`).Scan(&tenantID); err != nil {
			_ = input.Rollback(ctx)
			return out, err
		}
	}
	snapshot, readErr := agentaccounts.ReadDailyPolicyTx(ctx, input, tenantID, *person, now)
	if readErr != nil {
		if err := input.Rollback(ctx); err != nil {
			return out, err
		}
		return blockDaily(out, "daily_limit_unknown"), nil
	}
	if err := input.Commit(ctx); err != nil {
		return out, err
	}
	snapshot, allDenied, err := agentaccounts.ProjectDailySnapshot(ctx, tx, snapshot, q.ProjectID)
	if err != nil {
		return out, err
	}
	// A catalog preview before accounts have been enrolled stays a preview.
	// A saved daily setting already requires complete plan evidence.
	if len(snapshot.DailyState) == 0 && len(snapshot.Daily) == 0 {
		return out, nil
	}
	if q.Harness != "" && allDenied[q.Harness] {
		return blockDaily(out, "context"), nil
	}
	trace := []Candidate{}
	for attempts := 0; attempts < 7 && out.Profile != nil; attempts++ {
		harness := out.Profile.Harness
		if allDenied[harness] {
			if q.Harness != "" {
				return blockDaily(out, "context"), nil
			}
			for _, c := range out.Ladder {
				c.Selected = false
				if c.ProfileID == out.Profile.ID {
					c.SkipReasons = append(c.SkipReasons, agentaccounts.ContextSkipReason)
				}
				trace = append(trace, c)
			}
			q.OffHarnesses = append(q.OffHarnesses, harness)
			out, err = resolve(q)
			if err != nil {
				return out, err
			}
			continue
		}
		daily := agentplan.DailyStart(snapshot, harness, now)
		if daily.Reason == "" {
			qualified, err := dailyQualified(ctx, tx, out, q.ProjectID, now)
			if err != nil {
				return out, err
			}
			ids := []string{}
			doors := []agentplan.DailyAccount{}
			for _, a := range snapshot.DailyState[harness].Accounts {
				if !slices.Contains(qualified, a.AccountID) {
					continue
				}
				doors = append(doors, a)
				if agentplan.DailyAccountReason(a, now) == "" {
					ids = append(ids, a.AccountID)
				}
			}
			if len(ids) > 0 {
				out.Trace.QualifyingAccountIDs = ids
				out.Ladder = append(trace, out.Ladder...)
				return out, nil
			}
			// Unqualified room in a sibling door cannot authorize this profile.
			narrowed := snapshot
			narrowed.DailyState = map[string]agentplan.DailyState{harness: agentplan.SummarizeDaily(doors)}
			daily = agentplan.DailyStart(narrowed, harness, now)
		}
		if !daily.FollowLadder || q.Harness != "" {
			return blockDaily(out, daily.Reason, &daily), nil
		}
		for _, c := range out.Ladder {
			c.Selected = false
			if c.ProfileID == out.Profile.ID {
				c.SkipReasons = append(c.SkipReasons, agentaccounts.ModelWaitReason(harness, &agentaccounts.CapacityWait{Code: daily.Reason, Until: daily.Until}))
			}
			trace = append(trace, c)
		}
		q.OffHarnesses = append(q.OffHarnesses, harness)
		out, err = resolve(q)
		if err != nil {
			return out, err
		}
	}
	out.Ladder = append(trace, out.Ladder...)
	if len(trace) > 0 {
		return blockDaily(out, "daily_limit"), nil
	}
	return out, nil
}

// dailyQualified is the set of accounts the resolved role admits for the
// selected profile. The daily ceiling only narrows it: headroom on a sibling
// that the role would not use never keeps an exhausted route selected.
func dailyQualified(ctx context.Context, tx pgx.Tx, out WorkResolution, projectID string, now time.Time) ([]string, error) {
	profile := out.Profile
	if strings.HasPrefix(out.Role, "review-gate") {
		return reviewQualifiedAccountIDs(ctx, tx, *profile, projectID, out.Residency, now)
	}
	// The resolver already qualified accounts for this profile, including any
	// narrowing of its own such as escalation room.
	if len(out.Trace.QualifyingAccountIDs) > 0 {
		return out.Trace.QualifyingAccountIDs, nil
	}
	return agentaccounts.QualifyingAccountIDs(ctx, tx, profile.ID, profile.Harness, projectID, out.Residency, now)
}

func blockDaily(out WorkResolution, reason string, decisions ...*agentplan.DailyDecision) WorkResolution {
	text := reason
	if reason == "daily_limit" || reason == "daily_limit_unknown" {
		harness := ""
		if out.Profile != nil {
			harness = out.Profile.Harness
		}
		wait := &agentaccounts.CapacityWait{Code: reason}
		if len(decisions) > 0 {
			wait.Until = decisions[0].Until
		}
		text = agentaccounts.ModelWaitReason(harness, wait)
	}
	out.Profile, out.CommandTemplate, out.OwnerRequired = nil, "", true
	out.Trace.Blocked = reason
	out.Trace.QualifyingAccountIDs = []string{}
	for i := range out.Ladder {
		if out.Ladder[i].Selected {
			out.Ladder[i].SkipReasons = append(out.Ladder[i].SkipReasons, text)
		}
		out.Ladder[i].Selected = false
	}
	return out
}
