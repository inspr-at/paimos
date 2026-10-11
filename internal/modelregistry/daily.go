// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/accountprivacy"
	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// resolveDailyWork admits each route inside the Models walk before moving to
// the next model. Reservation rechecks owner policy under its write fence.
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
	// Models already checked each account owner's daily plan route by route.
	// The legacy wrapper remains only until saved ladders migrate in S2.
	if out.Trace.PreferenceOf != nil {
		return out, nil
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

// modelsDailyAdmission narrows the already-qualified accounts per route. The
// owner's snapshot is reused across native levels and model steps, without
// publishing another person's plan or quota to the current model reader.
type modelsDailyAdmission struct {
	principal tenant.Principal
	snapshots map[string]agentplan.Snapshot
	unknown   map[string]bool
	wait      bool
	blocked   string
}

func (d *modelsDailyAdmission) filter(ctx context.Context, tx pgx.Tx, profile Profile, ids []string, now time.Time) ([]string, string, error) {
	d.wait = false
	d.blocked = ""
	if len(ids) == 0 {
		return ids, "", nil
	}
	if len(ids) > 256 {
		return nil, "", modelprefs.ErrBoardBounds
	}
	rows, err := tx.Query(ctx, `SELECT id::text,owner_person_id::text FROM agent_accounts WHERE id=ANY($1::uuid[]) ORDER BY id LIMIT 257`, ids)
	if err != nil {
		return nil, "", err
	}
	type owned struct {
		id    string
		owner *string
	}
	accounts := []owned{}
	for rows.Next() {
		var a owned
		if err := rows.Scan(&a.id, &a.owner); err != nil {
			rows.Close()
			return nil, "", err
		}
		accounts = append(accounts, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	visible := accountprivacy.Policy{}
	if d.principal.ID != "" {
		visible, err = accountprivacy.Load(ctx, tx, d.principal, ids)
		if err != nil {
			return nil, "", err
		}
	}
	tenantID := d.principal.TenantID
	if tenantID == "" {
		if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')`).Scan(&tenantID); err != nil {
			return nil, "", err
		}
	}
	kept := []string{}
	firstCause := ""
	for _, a := range accounts {
		// Unlinked accounts have no owner daily plan, exactly as reservation's
		// dailyAccountWaitTx. Their ordinary schedules and quotas still apply.
		if a.owner == nil {
			kept = append(kept, a.id)
			continue
		}
		snapshot, found := d.snapshots[*a.owner]
		if !found && !d.unknown[*a.owner] {
			input, err := tx.Begin(ctx)
			if err != nil {
				return nil, "", err
			}
			snapshot, err = agentaccounts.ReadDailyPolicyTx(ctx, input, tenantID, *a.owner, now)
			if err != nil {
				if rollback := input.Rollback(ctx); rollback != nil {
					return nil, "", rollback
				}
				if ctx.Err() != nil {
					return nil, "", ctx.Err()
				}
				d.unknown[*a.owner] = true
			} else {
				if err := input.Commit(ctx); err != nil {
					return nil, "", err
				}
				d.snapshots[*a.owner] = snapshot
			}
		}
		reason := "daily_limit_unknown"
		for _, item := range snapshot.DailyState[profile.Harness].Accounts {
			if item.AccountID == a.id {
				reason = agentplan.DailyAccountReason(item, now)
				// An unreadable plan still refuses above. Missing or stale quota
				// is unknown, rather than evidence of a reached daily cap.
				if reason == "daily_limit_unknown" {
					reason = ""
				}
				break
			}
		}
		if reason == "" {
			kept = append(kept, a.id)
			continue
		}
		// Until S2 carries saved Wait settings into order ends, exhaust this
		// model's sibling routes but do not advance to another model at a cap.
		if reason == "daily_limit_unknown" || reason == "daily_limit" && snapshot.Daily[profile.Harness].AtLimit == "wait" {
			d.wait = true
		}
		if firstCause == "" {
			d.blocked = reason
			wait := &agentaccounts.CapacityWait{Code: reason}
			if reason == "daily_limit" {
				wait.Until = &snapshot.DailyUntil
			}
			if !visible[a.id] {
				wait = &agentaccounts.CapacityWait{}
			}
			firstCause = agentaccounts.ModelWaitReason(profile.Harness, wait)
		}
	}
	if len(kept) > 0 {
		return kept, "", nil
	}
	return kept, firstCause, nil
}
