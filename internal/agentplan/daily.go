// SPDX-License-Identifier: AGPL-3.0-only
package agentplan

import (
	"encoding/json"
	"errors"
	"math"
	"time"
)

const DefaultDailyPoints = 10

var ErrDailyWrite = errors.New("invalid daily plan write")

type DailyPace struct {
	Mode         string `json:"mode"`
	PointsPerDay *int   `json:"points_per_day"`
}
type DailyBoost struct {
	LimitUsedPct float64   `json:"limit_used_pct"`
	EnteredAs    string    `json:"entered_as"`
	Until        time.Time `json:"until"`
}
type DailySettings struct {
	Pace       DailyPace   `json:"pace"`
	BoostToday *DailyBoost `json:"boost_today"`
	AtLimit    string      `json:"at_limit"`
}

func DefaultDaily() DailySettings {
	return DailySettings{Pace: DailyPace{Mode: "pace"}, AtLimit: "ladder"}
}
func (p Plan) DailySetting(harness string) DailySettings {
	if settings, ok := p.Daily[harness]; ok {
		return settings
	}
	return DefaultDaily()
}
func (d DailySettings) Validate() error {
	if d.Pace.Mode != "pace" && d.Pace.Mode != "everything" || d.AtLimit != "ladder" && d.AtLimit != "wait" {
		return errors.New("daily pace or at_limit is invalid")
	}
	if d.Pace.PointsPerDay != nil && (*d.Pace.PointsPerDay < 1 || *d.Pace.PointsPerDay > 50) {
		return errors.New("daily points_per_day must be null or 1 to 50")
	}
	if b := d.BoostToday; b != nil {
		if !percent(b.LimitUsedPct) || b.EnteredAs != "used" && b.EnteredAs != "left" || b.Until.IsZero() {
			return errors.New("invalid daily boost")
		}
	}
	return nil
}

// Required nullable fields must be present. A missing used percentage must
// never decode to zero and silently lower or reopen a daily allowance.
func (d *DailySettings) UnmarshalJSON(raw []byte) error {
	type plain DailySettings
	var out plain
	if err := strictJSON(raw, &out); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return errors.New("daily setting must be an object")
	}
	for _, key := range []string{"pace", "boost_today", "at_limit"} {
		if _, ok := fields[key]; !ok {
			return errors.New("daily setting fields are required")
		}
	}
	var pace map[string]json.RawMessage
	if err := json.Unmarshal(fields["pace"], &pace); err != nil {
		return err
	}
	if _, ok := pace["points_per_day"]; !ok {
		return errors.New("daily points_per_day is required")
	}
	if out.BoostToday != nil {
		var boost map[string]json.RawMessage
		if err := json.Unmarshal(fields["boost_today"], &boost); err != nil {
			return err
		}
		for _, key := range []string{"limit_used_pct", "entered_as", "until"} {
			if v, ok := boost[key]; !ok || string(v) == "null" {
				return errors.New("daily boost fields are required")
			}
		}
	}
	*d = DailySettings(out)
	return d.Validate()
}
func SameDaily(a, b DailySettings) bool {
	if a.Pace.Mode != b.Pace.Mode || a.AtLimit != b.AtLimit || (a.Pace.PointsPerDay == nil) != (b.Pace.PointsPerDay == nil) {
		return false
	}
	if a.Pace.PointsPerDay != nil && *a.Pace.PointsPerDay != *b.Pace.PointsPerDay {
		return false
	}
	if (a.BoostToday == nil) != (b.BoostToday == nil) {
		return false
	}
	if a.BoostToday != nil && (a.BoostToday.LimitUsedPct != b.BoostToday.LimitUsedPct || a.BoostToday.EnteredAs != b.BoostToday.EnteredAs || !a.BoostToday.Until.Equal(b.BoostToday.Until)) {
		return false
	}
	return true
}

// LocalDay uses calendar arithmetic, including 23/25-hour DST days.
func LocalDay(now time.Time, zone string) (time.Time, time.Time, error) {
	if zone == "" {
		zone = "UTC"
	}
	if zone == "Local" || len(zone) > 128 {
		return time.Time{}, time.Time{}, errors.New("invalid daily timezone")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return start.UTC(), start.AddDate(0, 0, 1).UTC(), nil
}
func percent(v float64) bool    { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 100 }
func Number(v float64) *float64 { return &v }

type DailyAccount struct {
	AccountID          string     `json:"account_id"`
	Label              string     `json:"label"`
	Order              int        `json:"order"`
	UsedPct            *float64   `json:"used_pct"`
	LeftPct            *float64   `json:"left_pct"`
	StartOfDayUsedPct  *float64   `json:"start_of_day_used_pct"`
	LimitUsedPct       *float64   `json:"limit_used_pct"`
	FloorPct           *float64   `json:"floor_pct"`
	ResetsAt           *time.Time `json:"resets_at"`
	ReadAt             *time.Time `json:"read_at"`
	Freshness          string     `json:"freshness"`
	Resets             any        `json:"resets"`
	ResetPolicy        string     `json:"reset_policy"`
	ResetPlan          any        `json:"reset_plan"`
	Routable           bool       `json:"routable"`
	DetailsRedacted    bool       `json:"details_redacted"`
	CanEdit            bool       `json:"can_edit"`
	ResetPacePoints    float64    `json:"-"`
	NoDailyLimit       bool       `json:"no_daily_limit,omitempty"`
	TodayPointsAllowed *float64   `json:"-"`
	OverPacePoints     *float64   `json:"-"`
}
type DailyState struct {
	State              string         `json:"state"`
	LimitUsedPct       *float64       `json:"limit_used_pct"`
	TodayPointsUsed    *float64       `json:"today_points_used"`
	TodayPointsAllowed *float64       `json:"today_points_allowed"`
	OverPacePoints     *float64       `json:"over_pace_points"`
	ActiveAccountID    *string        `json:"active_account_id"`
	NextOnLadder       *string        `json:"next_on_ladder"`
	Accounts           []DailyAccount `json:"accounts"`
}

func ApplyDaily(a *DailyAccount, d DailySettings, defaultPoints int, now time.Time) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if defaultPoints < 1 || defaultPoints > 50 {
		return errors.New("invalid daily default")
	}
	if a.UsedPct == nil {
		return nil
	}
	if !percent(*a.UsedPct) {
		return errors.New("invalid daily usage")
	}
	a.LeftPct = Number(100 - *a.UsedPct)
	if a.StartOfDayUsedPct == nil || a.FloorPct == nil {
		return nil
	}
	// A later sample may restate the same reset downward. Keep the reading and
	// treat today's consumption as zero rather than failing the whole plan.
	if !percent(*a.UsedPct) || !percent(*a.StartOfDayUsedPct) || !percent(*a.FloorPct) {
		return errors.New("invalid daily usage")
	}
	points := defaultPoints
	if d.Pace.PointsPerDay != nil {
		points = *d.Pace.PointsPerDay
	}
	base := *a.StartOfDayUsedPct
	if math.IsNaN(a.ResetPacePoints) || math.IsInf(a.ResetPacePoints, 0) || a.ResetPacePoints < 0 || a.ResetPacePoints > 50 {
		return errors.New("invalid reset pace")
	}
	effectivePoints := min(50, float64(points)+a.ResetPacePoints)
	limit := base + effectivePoints
	if d.Pace.Mode == "everything" {
		limit = 100
	}
	if b := d.BoostToday; b != nil && now.Before(b.Until) {
		limit = b.LimitUsedPct
	}
	a.LimitUsedPct = Number(min(100-*a.FloorPct, limit))
	a.TodayPointsAllowed = Number(max(0, *a.LimitUsedPct-base))
	a.OverPacePoints = Number(0)
	if d.Pace.Mode == "pace" {
		a.OverPacePoints = Number(max(0, *a.UsedPct-base-effectivePoints))
	}
	return nil
}

// Unknown/private usage has no numeric headroom. A harness reaches its limit
// only after all its doors are exhausted; select the first routable door below
// its limit, falling back to a known door for the dial's reading.
func SummarizeDaily(accounts []DailyAccount) DailyState {
	out := DailyState{State: "at_limit", Accounts: accounts}
	active := -1
	roomPick := -1
	allNoLimit := len(accounts) > 0
	for i, a := range accounts {
		allNoLimit = allNoLimit && a.NoDailyLimit && !a.DetailsRedacted
		if a.UsedPct != nil && active < 0 {
			active = i
		}
		room := a.NoDailyLimit && !a.DetailsRedacted || a.UsedPct != nil && a.LimitUsedPct != nil && a.Freshness == "fresh" && *a.UsedPct < *a.LimitUsedPct
		if room {
			out.State = "on_pace"
			if roomPick < 0 || a.Routable && !accounts[roomPick].Routable {
				roomPick = i
			}
		}
	}
	if allNoLimit {
		out.State = "no_limit"
	}
	if roomPick >= 0 {
		active = roomPick
	}
	if active < 0 && len(accounts) > 0 {
		active = 0
	}
	if active >= 0 {
		a := accounts[active]
		out.ActiveAccountID = &a.AccountID
		out.LimitUsedPct = a.LimitUsedPct
		out.TodayPointsAllowed = a.TodayPointsAllowed
		out.OverPacePoints = a.OverPacePoints
		if a.UsedPct != nil && a.StartOfDayUsedPct != nil {
			out.TodayPointsUsed = Number(max(0, *a.UsedPct-*a.StartOfDayUsedPct))
		}
		if out.State == "on_pace" && a.OverPacePoints != nil && *a.OverPacePoints > 0 {
			out.State = "over_pace"
		}
	}
	return out
}
