// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func validBoost(n int) bool { return n == 0 || n == 10 || n == 20 || n == 30 }

func boostExpiry(now time.Time, zone string) (time.Time, error) {
	if zone == "" || len(zone) > 128 || zone == "Local" {
		return time.Time{}, fail(400, "IANA timezone required")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, fail(400, "invalid timezone")
	}
	local := now.In(loc)
	until := time.Date(local.Year(), local.Month(), local.Day(), 23, 59, 0, 0, loc)
	return until.UTC(), nil
}

// A burst increases the paced share, never vendor capacity, the hard floor or
// Keep for you. Hold and closed work bands remain authoritative.
func boostPacing(p capacity.Pacing, remaining float64, boost int, s capacity.Schedule, now time.Time) capacity.Pacing {
	if boost == 0 || s.ActiveOverride(now) == "hold" {
		return p
	}
	p.BudgetPercent = min(100, p.BudgetPercent+float64(boost))
	p.SuggestedTodayPercent = max(0, min(remaining, p.BudgetPercent-p.UsedTodayPercent))
	next := s.NextStart(now, p.AllowOff)
	override := s.ActiveOverride(now)
	if override == "sprint" || override == "away" || next != nil && next.Equal(now) {
		p.AvailableNowPercent = max(0, min(p.SuggestedTodayPercent, remaining-p.ReserveEffectivePercent))
	}
	p.Ahead = p.UsedTodayPercent > p.BudgetPercent+.5
	return p
}

type boostAccountRevision struct {
	ID       string `json:"account_id"`
	Revision *int64 `json:"revision"`
	Binding  *int64 `json:"binding_revision"`
}

func (m *Module) writeBoost(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writeErr(w, fail(403, "person required"))
		return
	}
	var in struct {
		Percent  *int                   `json:"boost_percent"`
		Timezone string                 `json:"timezone"`
		Accounts []boostAccountRevision `json:"accounts"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if in.Percent == nil || !validBoost(*in.Percent) || len(in.Accounts) < 1 || len(in.Accounts) > 1024 {
		writeErr(w, fail(400, "boost must be 0, 10, 20 or 30 with 1 to 1024 accounts"))
		return
	}
	seen := map[string]bool{}
	for i := range in.Accounts {
		a := &in.Accounts[i]
		a.ID = strings.ToLower(a.ID)
		if !uuidRE.MatchString(a.ID) || seen[a.ID] || a.Revision == nil || a.Binding == nil || *a.Revision < 0 || *a.Binding < 0 {
			writeErr(w, fail(400, "unique account IDs and revisions required"))
			return
		}
		seen[a.ID] = true
	}
	sort.Slice(in.Accounts, func(i, j int) bool { return in.Accounts[i].ID < in.Accounts[j].ID })
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out := struct {
		Accounts []usagePolicy `json:"accounts"`
	}{Accounts: []usagePolicy{}}
	err := m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='5s'`); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "account.manage", authz.Scope{}); err != nil {
			return fail(403, "account management required")
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		expiry, err := boostExpiry(now, in.Timezone)
		if err != nil {
			return err
		}
		var until *time.Time
		if *in.Percent != 0 {
			if !now.Before(expiry) {
				return fail(409, "today's boost has ended")
			}
			until = &expiry
		}
		before := make([]usagePolicy, 0, len(in.Accounts))
		for _, requested := range in.Accounts {
			a, err := lockAccount(ctx, tx, requested.ID)
			if err != nil {
				return err
			}
			u, err := loadUsagePolicy(ctx, tx, a.ID)
			if err != nil {
				return err
			}
			if err := m.usagePermissions(ctx, tx, p, a, &u); err != nil {
				return err
			}
			if !u.CanSetPosture {
				return fail(403, "account owner required")
			}
			if u.Revision != *requested.Revision || u.BindingRevision != *requested.Binding {
				return fail(409, "account policy or binding changed")
			}
			before = append(before, u)
		}
		// Ownership can change while the screen is open. A partial account list must
		// not silently claim that all of the person's accounts were boosted.
		rows, err := tx.Query(ctx, `SELECT id::text FROM agent_accounts WHERE NOT `+retiredSQL+` AND `+modelprefs.CanonicalPersonSQL("owner_person_id")+`=`+modelprefs.CanonicalPersonSQL("$1::uuid")+` ORDER BY id LIMIT 1025`, p.ID)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if len(ids) != len(in.Accounts) {
			return fail(409, "owned account list changed")
		}
		for i, id := range ids {
			if id != in.Accounts[i].ID {
				return fail(409, "owned account list changed")
			}
		}
		for _, u := range before {
			if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET boost_percent=$2,boost_until=$3,boost_person_id=`+modelprefs.CanonicalPersonSQL("owner_person_id")+`,boost_link_revision=link_revision,usage_revision=$4 WHERE id=$1`, u.AccountID, *in.Percent, until, u.Revision+1); err != nil {
				return err
			}
			saved, err := loadUsagePolicy(ctx, tx, u.AccountID)
			if err != nil {
				return err
			}
			saved.CanSetPosture, saved.CanSetFloor = u.CanSetPosture, u.CanSetFloor
			out.Accounts = append(out.Accounts, saved)
		}
		// All row locks and writes precede the single event-counter acquisition.
		_, err = events.Append(ctx, tx, p, events.Change{Type: "account.boost_changed", Before: before, After: out.Accounts})
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
