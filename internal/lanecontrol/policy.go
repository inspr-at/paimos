// SPDX-License-Identifier: AGPL-3.0-only

// Package lanecontrol is the transaction boundary for lane resource grants.
// Scheduler callers hold authz.LockProjectMutation before any record locks,
// and append audit events only after the returned resource mutations. Nothing
// here schedules work or grants workers permission to change lane policy.
package lanecontrol

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"
	_ "time/tzdata"

	"github.com/inspr-at/paimos/internal/laneprotocol"
)

const Capability = laneprotocol.Capability
const StopAllowanceMS = laneprotocol.StopAllowanceMS
const maxBudgetMS int64 = 36_000_000_000

type Grant = laneprotocol.Grant
type Settlement = laneprotocol.Settlement

type window struct {
	Timezone string `json:"timezone"`
	Days     []int  `json:"days"`
	Start    string `json:"start"`
	End      string `json:"end"`
}
type policy struct {
	ParallelLimit int `json:"parallel_limit"`
	Budget        struct {
		AgentHours float64 `json:"agent_hours"`
	} `json:"budget"`
	Window window `json:"window"`
}

func parsePolicy(raw []byte) (policy, int64, error) {
	var p policy
	if len(raw) > 16384 || json.Unmarshal(raw, &p) != nil || p.ParallelLimit < 1 || p.ParallelLimit > 20 || math.IsNaN(p.Budget.AgentHours) || math.IsInf(p.Budget.AgentHours, 0) || p.Budget.AgentHours <= 0 || p.Budget.AgentHours > 10000 {
		return p, 0, errors.New("invalid lane resource policy")
	}
	return p, int64(math.Floor(p.Budget.AgentHours * 3600000)), nil
}

// activeWindow rejects ambiguous or nonexistent endpoints instead of inventing
// extra authorization across a clock transition. A normal overnight instance
// retains the same UTC identity on both sides of midnight (and DST).
func activeWindow(w window, now time.Time) (time.Time, time.Time, error) {
	bad := errors.New("lane window closed or ambiguous")
	if w.Timezone == "" || w.Timezone == "Local" || len(w.Days) < 1 || len(w.Days) > 7 {
		return time.Time{}, time.Time{}, bad
	}
	loc, err := time.LoadLocation(w.Timezone)
	if err != nil {
		return time.Time{}, time.Time{}, bad
	}
	parse := func(s string) (int, int, bool) {
		if len(s) != 5 || s[2] != ':' || s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' || s[3] < '0' || s[3] > '9' || s[4] < '0' || s[4] > '9' {
			return 0, 0, false
		}
		h, e := strconv.Atoi(s[:2])
		m, f := strconv.Atoi(s[3:])
		return h, m, e == nil && f == nil && h >= 0 && h < 24 && m >= 0 && m < 60
	}
	sh, sm, ok := parse(w.Start)
	eh, em, ok2 := parse(w.End)
	if !ok || !ok2 || w.Start == w.End {
		return time.Time{}, time.Time{}, bad
	}
	days := map[int]bool{}
	for _, d := range w.Days {
		if d < 1 || d > 7 || days[d] {
			return time.Time{}, time.Time{}, bad
		}
		days[d] = true
	}
	local := now.In(loc)
	endpoint := func(day time.Time, h, m int) (time.Time, bool) {
		v := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, loc)
		if v.Day() != day.Day() || v.Hour() != h || v.Minute() != m {
			return v, false
		}
		// IANA offset changes can be half an hour or several hours. Compare offsets
		// on either side and reject both copies of a repeated wall-clock endpoint.
		_, before := v.Add(-24 * time.Hour).Zone()
		_, after := v.Add(24 * time.Hour).Zone()
		delta := time.Duration(before-after) * time.Second
		if delta != 0 {
			for _, other := range []time.Time{v.Add(delta), v.Add(-delta)} {
				if other.Format("2006-01-02 15:04") == v.Format("2006-01-02 15:04") {
					return v, false
				}
			}
		}
		return v, true
	}
	for _, offset := range []int{-1, 0} {
		day := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, loc).AddDate(0, 0, offset)
		weekday := int(day.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		if !days[weekday] {
			continue
		}
		endDay := day
		if w.End < w.Start {
			endDay = day.AddDate(0, 0, 1)
		}
		start, a := endpoint(day, sh, sm)
		end, b := endpoint(endDay, eh, em)
		if a && b && !now.Before(start) && now.Before(end) {
			return start.UTC(), end.UTC(), nil
		}
	}
	return time.Time{}, time.Time{}, bad
}

// WorkingPreference is the existing AEON-540 dial contract. The active view
// selects its allocation; spare capacity is flexible, never borrowed from an
// unoccupied reserved row. Malformed saved values disable automatic starts.
type WorkingPreference struct {
	Cap   *int           `json:"cap,omitempty"`
	View  string         `json:"view,omitempty"`
	Area  map[string]int `json:"area,omitempty"`
	Model map[string]int `json:"model,omitempty"`
}

func ParseWorkingPreference(raw []byte) (WorkingPreference, error) {
	var p WorkingPreference
	if len(raw) > 16384 || json.Unmarshal(raw, &p) != nil || p.Cap != nil && (*p.Cap < 1 || *p.Cap > 12) || p.View != "" && p.View != "area" && p.View != "model" {
		return p, errors.New("invalid agents.working target")
	}
	for _, m := range []map[string]int{p.Area, p.Model} {
		if len(m) > 32 {
			return p, errors.New("too many slot allocations")
		}
		sum := 0
		for k, n := range m {
			if len(k) > 80 || n < 0 || n > 12 {
				return p, errors.New("invalid slot allocation")
			}
			sum += n
		}
		if sum > 0 && (p.Cap == nil || sum > *p.Cap) {
			return p, errors.New("allocations exceed target")
		}
	}
	return p, nil
}

type slot struct{ area, harness string }

func fitsSlots(p WorkingPreference, running []slot, next slot) bool {
	if p.Cap == nil || len(running) >= *p.Cap {
		return false
	}
	targets := p.Area
	key := func(s slot) string { return s.area }
	if p.View == "model" {
		targets = p.Model
		key = func(s slot) string { return s.harness }
	}
	assigned := 0
	for _, v := range targets {
		assigned += v
	}
	counts := map[string]int{}
	for _, s := range running {
		counts[key(s)]++
	}
	counts[key(next)]++
	flexibleUsed := 0
	for k, n := range counts {
		flexibleUsed += max(0, n-targets[k])
	}
	return flexibleUsed <= *p.Cap-assigned
}
