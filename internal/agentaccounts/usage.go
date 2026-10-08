// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
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

type usagePolicy struct {
	AccountID       string     `json:"account_id"`
	Posture         string     `json:"posture"`
	Source          string     `json:"source"`
	Floor           int        `json:"floor_percent"`
	OwnFloor        int        `json:"own_floor_percent"`
	Revision        int64      `json:"revision"`
	BindingRevision int64      `json:"binding_revision"`
	CanSetPosture   bool       `json:"can_set_posture"`
	CanSetFloor     bool       `json:"can_set_floor"`
	Boost           int        `json:"boost_percent"`
	BoostUntil      *time.Time `json:"boost_until"`
	active          bool
}

func validPosture(s string) bool { return s == "careful" || s == "balanced" || s == "maxout" }

// The account's canonical owner chooses usage, never the run's requester or
// registering agent. An old explicit capacity schedule remains authoritative
// until a stored usage preference exists.
func loadUsagePolicy(ctx context.Context, tx pgx.Tx, id string) (usagePolicy, error) {
	p := usagePolicy{AccountID: id, Posture: "balanced", Source: "schedule"}
	var account, person, workspace *string
	now, err := dbNow(ctx, tx)
	if err != nil {
		return p, err
	}
	err = tx.QueryRow(ctx, `SELECT
 CASE WHEN a.usage_posture_person_id=`+modelprefs.CanonicalPersonSQL("a.owner_person_id")+` AND a.usage_posture_link_revision=a.link_revision THEN a.usage_posture END,
 (SELECT usage FROM model_pref_profiles WHERE scope='person' AND person_id=`+modelprefs.CanonicalPersonSQL("a.owner_person_id")+`),
 (SELECT usage FROM model_pref_profiles WHERE scope='workspace'),
 COALESCE(a.usage_floor_percent,0),COALESCE(a.usage_revision,0),a.link_revision,
 (SELECT COALESCE(max(usage_floor_percent),0) FROM agent_accounts WHERE id IN (`+quotaAccounts+`)),
 CASE WHEN a.boost_person_id=`+modelprefs.CanonicalPersonSQL("a.owner_person_id")+` AND a.boost_link_revision=a.link_revision AND a.boost_until>$2 THEN COALESCE(a.boost_percent,0) ELSE 0 END,
 CASE WHEN a.boost_person_id=`+modelprefs.CanonicalPersonSQL("a.owner_person_id")+` AND a.boost_link_revision=a.link_revision AND a.boost_until>$2 AND a.boost_percent>0 THEN a.boost_until END
 FROM agent_accounts a WHERE a.id=$1`, id, now).Scan(&account, &person, &workspace, &p.OwnFloor, &p.Revision, &p.BindingRevision, &p.Floor, &p.Boost, &p.BoostUntil)
	if isNoRows(err) {
		return p, fail(404, "account not found")
	}
	if err != nil {
		return p, err
	}
	for _, v := range []struct {
		value  *string
		source string
	}{{account, "account"}, {person, "person"}, {workspace, "workspace"}} {
		if v.value != nil {
			if !validPosture(*v.value) {
				return p, fail(503, "invalid stored usage posture")
			}
			p.Posture, p.Source, p.active = *v.value, v.source, true
			break
		}
	}
	if p.Floor < 0 || p.Floor > 80 {
		return p, fail(503, "invalid stored usage floor")
	}
	if !validBoost(p.Boost) {
		return p, fail(503, "invalid stored boost")
	}
	return p, nil
}

func usageSchedule(s capacity.Schedule, p usagePolicy, now time.Time) capacity.Schedule {
	if !p.active {
		return s
	}
	// Explicit actions on Accounts and computers outrank the base posture;
	// admission still applies the hard floor after any Sprint or Run now.
	override := s.ActiveOverride(now)
	s.Nights = false
	switch p.Posture {
	case "careful":
		reserve := 30.0
		if s.Reserve == capacity.ReserveFixed && s.ReservePercent >= 10 && s.ReservePercent <= 80 {
			reserve = s.ReservePercent
		}
		s.OffDays = "rest"
		s.Reserve = capacity.ReserveFixed
		s.ReservePercent = reserve
	case "balanced":
		s.OffDays = "expire"
		s.Reserve = capacity.ReserveAuto
		s.ReservePercent = 0
	case "maxout":
		s.OffDays = "normal"
		s.Reserve = capacity.ReserveOff
		s.ReservePercent = 0
	}
	if override == "hold" || override == "away" || override == "sprint" {
		return s
	}
	s.Override, s.OverrideUntil = "", nil
	if p.Posture == "maxout" {
		// Derive Sprint each read, bounded by current windows rather than storing
		// an expired override. Floors are a separate hard gate below Plan.
		s.Override = "sprint"
	}
	return s
}

func applyUsageFloor(windows []Window, p usagePolicy) {
	for i := range windows {
		w := &windows[i]
		w.usagePosture = p.Posture
		if !p.active {
			w.usagePosture = ""
		}
		if synthetic(*w) {
			continue
		} // Unknown usage cannot invent a numeric floor.
		if p.Floor > 0 {
			ceiling := allowedUnits(w.Allowance, float64(100-p.Floor)/100)
			w.usageCeiling = &ceiling
		}
		if p.active && w.capacityReadAt != nil {
			w.PaceModel, w.BurstRatio = "steady", 0
			if p.Posture == "maxout" {
				w.PaceModel = "unrestricted"
			}
		}
		w.BurstRatio = min(1, w.BurstRatio+float64(p.Boost)/100)
	}
}

func (m *Module) usagePermissions(ctx context.Context, tx pgx.Tx, p tenant.Principal, a Account, u *usagePolicy) error {
	if p.Kind != tenant.Person || authz.RequireTx(ctx, tx, p, "account.manage", authz.Scope{}) != nil {
		return nil
	}
	var owns bool
	if err := tx.QueryRow(ctx, `SELECT COALESCE(`+modelprefs.CanonicalPersonSQL("$1::uuid")+`=`+modelprefs.CanonicalPersonSQL("$2::uuid")+`,false)`, p.ID, a.OwnerPersonID).Scan(&owns); err != nil {
		return err
	}
	u.CanSetPosture = owns
	u.CanSetFloor = authz.RequireTx(ctx, tx, p, "model_prefs.manage", authz.Scope{}) == nil
	return nil
}

func (m *Module) writeUsagePolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writeErr(w, fail(403, "person required"))
		return
	}
	id := r.PathValue("accountId")
	if !uuidRE.MatchString(id) {
		writeErr(w, fail(404, "account not found"))
		return
	}
	floor := strings.HasSuffix(r.URL.Path, "/floor")
	var revision, binding *int64
	var posture *string
	var percent *int
	if floor {
		var in struct {
			Floor    *int   `json:"floor_percent"`
			Revision *int64 `json:"revision"`
			Binding  *int64 `json:"binding_revision"`
		}
		if err := decodeJSON(w, r, &in); err != nil {
			writeErr(w, err)
			return
		}
		percent, revision, binding = in.Floor, in.Revision, in.Binding
		if percent == nil || *percent < 0 || *percent > 80 {
			writeErr(w, fail(400, "floor must be between 0 and 80"))
			return
		}
	} else {
		var in struct {
			Posture  json.RawMessage `json:"posture"`
			Revision *int64          `json:"revision"`
			Binding  *int64          `json:"binding_revision"`
		}
		if err := decodeJSON(w, r, &in); err != nil {
			writeErr(w, err)
			return
		}
		if len(in.Posture) == 0 || json.Unmarshal(in.Posture, &posture) != nil {
			writeErr(w, fail(400, "posture is required and must be a usage word or null"))
			return
		}
		revision, binding = in.Revision, in.Binding
		if posture != nil && !validPosture(*posture) {
			writeErr(w, fail(400, "invalid usage posture"))
			return
		}
	}
	if revision == nil || binding == nil || *revision < 0 || *binding < 0 {
		writeErr(w, fail(400, "revision and binding_revision required"))
		return
	}
	var out usagePolicy
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := authz.RequireTx(ctx, tx, p, "account.manage", authz.Scope{}); err != nil {
			return fail(403, "account management required")
		}
		a, err := lockAccount(ctx, tx, id)
		if err != nil {
			return err
		}
		before, err := loadUsagePolicy(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := m.usagePermissions(ctx, tx, p, a, &before); err != nil {
			return err
		}
		if floor && !before.CanSetFloor || !floor && !before.CanSetPosture {
			return fail(403, "account owner or workspace admin required")
		}
		if before.Revision != *revision || before.BindingRevision != *binding {
			return fail(409, "account policy or binding changed")
		}
		if floor {
			_, err = tx.Exec(ctx, `UPDATE agent_accounts SET usage_floor_percent=$2,usage_revision=$3 WHERE id=$1`, id, *percent, *revision+1)
		} else {
			_, err = tx.Exec(ctx, `UPDATE agent_accounts SET usage_posture=$2,usage_posture_person_id=`+modelprefs.CanonicalPersonSQL("owner_person_id")+`,usage_posture_link_revision=link_revision,usage_revision=$3 WHERE id=$1`, id, posture, *revision+1)
		}
		if err != nil {
			return err
		}
		out, err = loadUsagePolicy(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := m.usagePermissions(ctx, tx, p, a, &out); err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "account.usage_changed", Before: before, After: out})
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

type guardPosture struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	Order      int        `json:"order"`
	Posture    string     `json:"posture"`
	Floor      int        `json:"floor_percent"`
	Boost      int        `json:"boost_percent"`
	BoostUntil *time.Time `json:"boost_until"`
	Keep       float64    `json:"keep_for_you_percent"`
}

func (m *Module) postures(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	harness := r.URL.Query().Get("harness")
	after := strings.ToLower(r.URL.Query().Get("after"))
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			limit = 0
		}
	}
	if !validHarness(harness) || limit < 1 || limit > 100 || after != "" && !uuidRE.MatchString(after) {
		writeErr(w, fail(400, "invalid posture query"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out := struct {
		Accounts []guardPosture `json:"accounts"`
		More     bool           `json:"has_more"`
		Cursor   string         `json:"next_cursor,omitempty"`
		AsOf     time.Time      `json:"as_of"`
	}{Accounts: []guardPosture{}}
	err := m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='5s'`); err != nil {
			return err
		}
		reader := p
		if p.Kind == tenant.Agent {
			scopes, err := keyScopes(ctx, tx, r, p)
			if err != nil {
				return err
			}
			reader.Scopes = scopes
		}
		if err := authz.RequireTx(ctx, tx, reader, "account.read", authz.Scope{}); err != nil {
			return fail(403, "account read required")
		}
		all, err := overviewAccounts(ctx, tx)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(all))
		for _, a := range all {
			ids = append(ids, a.ID)
		}
		visible, err := accountprivacy.Load(ctx, tx, p, ids)
		if err != nil {
			return err
		}
		kept := all[:0]
		for _, a := range all {
			if a.Harness == harness && visible[a.ID] {
				kept = append(kept, a)
			}
		}
		all = kept
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		out.AsOf = now
		advice, err := routingAdvice(ctx, tx, all, "", runRow{Purpose: "managed"}, now)
		if err != nil {
			return err
		}
		for _, a := range all {
			if a.Harness != harness || a.ID <= after {
				continue
			}
			if len(out.Accounts) == limit {
				out.More = true
				out.Cursor = out.Accounts[len(out.Accounts)-1].ID
				break
			}
			u, err := loadUsagePolicy(ctx, tx, a.ID)
			if err != nil {
				return err
			}
			s, err := routingSchedule(ctx, tx, a)
			if err != nil {
				return err
			}
			keep := s.ReserveLevel(0)
			if s.Reserve == capacity.ReserveAuto || s.Reserve == "" {
				learned, err := loadLearning(ctx, tx, a.ID)
				if err != nil {
					return err
				}
				auto := 0.0
				for _, metric := range learned.summary(now, s).Windows {
					auto = max(auto, metric.AutoReserve)
				}
				keep = s.ReserveLevel(auto)
			}
			if u.Posture == "maxout" {
				keep = float64(u.Floor)
			}
			out.Accounts = append(out.Accounts, guardPosture{ID: a.ID, Label: a.Label, Order: advice[a.ID].Rank, Posture: u.Posture, Floor: u.Floor, Boost: u.Boost, BoostUntil: u.BoostUntil, Keep: keep})
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
