// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/accountprivacy"
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
	if in.Percent == nil || !validBoost(*in.Percent) || len(in.Accounts) > 1024 {
		writeErr(w, fail(400, "boost must be 0, 10, 20 or 30 with at most 1024 accounts"))
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
		Accounts      []usagePolicy `json:"accounts"`
		Withheld      int           `json:"withheld_count,omitempty"`
		WithheldUntil *time.Time    `json:"withheld_boost_until,omitempty"`
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
		// One sorted batch. The body names the privacy-visible owned accounts.
		// Withheld owned accounts stay locked and take the stored revision.
		rows, err := tx.Query(ctx, `SELECT id::text, COALESCE(owner_person_id::text,''), COALESCE(usage_revision,0), link_revision FROM agent_accounts WHERE NOT `+retiredSQL+` AND `+modelprefs.CanonicalPersonSQL("owner_person_id")+`=`+modelprefs.CanonicalPersonSQL("$1::uuid")+` ORDER BY id LIMIT 1025 FOR NO KEY UPDATE`, p.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		type ownedBoostRow struct {
			ID, Owner         string
			Revision, Binding int64
		}
		owned := []ownedBoostRow{}
		for rows.Next() {
			var row ownedBoostRow
			if err := rows.Scan(&row.ID, &row.Owner, &row.Revision, &row.Binding); err != nil {
				return err
			}
			owned = append(owned, row)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(owned) > 1024 {
			return fail(409, "owned account list changed")
		}
		ownedIDs := map[string]bool{}
		for _, row := range owned {
			ownedIDs[row.ID] = true
		}
		for _, requested := range in.Accounts {
			if !ownedIDs[requested.ID] {
				return fail(403, "account owner required")
			}
		}
		ids := make([]string, len(owned))
		for i, row := range owned {
			ids[i] = row.ID
		}
		policy := accountprivacy.Policy{}
		if len(ids) > 0 {
			policy, err = accountprivacy.Load(ctx, tx, p, ids)
			if err != nil {
				return err
			}
		}
		visible := make([]ownedBoostRow, 0, len(owned))
		for _, row := range owned {
			if policy[row.ID] {
				visible = append(visible, row)
			}
		}
		if len(visible) != len(in.Accounts) {
			return fail(409, "owned account list changed")
		}
		for i, row := range visible {
			requested := in.Accounts[i]
			if row.ID != requested.ID {
				return fail(409, "owned account list changed")
			}
			if row.Revision != *requested.Revision || row.Binding != *requested.Binding {
				return fail(409, "account policy or binding changed")
			}
		}
		if len(owned) == 0 {
			return nil
		}
		before := make([]usagePolicy, 0, len(owned))
		for _, row := range owned {
			u, err := loadUsagePolicy(ctx, tx, row.ID)
			if err != nil {
				return err
			}
			if u.Revision != row.Revision || u.BindingRevision != row.Binding {
				return fail(503, "account revision changed under lock")
			}
			ownerID := row.Owner
			account := Account{ID: row.ID, OwnerPersonID: &ownerID}
			if err := m.usagePermissions(ctx, tx, p, account, &u); err != nil {
				return err
			}
			if !u.CanSetPosture {
				return fail(403, "account owner required")
			}
			before = append(before, u)
		}
		after := make([]usagePolicy, 0, len(before))
		withheld := 0
		for _, u := range before {
			if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET boost_percent=$2,boost_until=$3,boost_person_id=`+modelprefs.CanonicalPersonSQL("owner_person_id")+`,boost_link_revision=link_revision,usage_revision=$4 WHERE id=$1`, u.AccountID, *in.Percent, until, u.Revision+1); err != nil {
				return err
			}
			saved, err := loadUsagePolicy(ctx, tx, u.AccountID)
			if err != nil {
				return err
			}
			saved.CanSetPosture, saved.CanSetFloor = u.CanSetPosture, u.CanSetFloor
			after = append(after, saved)
			if policy[u.AccountID] {
				out.Accounts = append(out.Accounts, saved)
				continue
			}
			withheld++
		}
		out.Withheld = withheld
		if withheld > 0 && until != nil {
			out.WithheldUntil = until
		}
		// All row locks and writes precede the single event-counter acquisition.
		_, err = events.Append(ctx, tx, p, events.Change{Type: "account.boost_changed", Before: before, After: after})
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
