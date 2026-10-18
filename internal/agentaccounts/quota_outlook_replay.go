// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"math"
	"sort"
	"time"
)

// AEON-1497: the quota outlook replays the routing policy over measured
// weekly readings. The soonest-resetting usable account runs; an account is
// full at its limit (100 minus its floor); held accounts never run and stay out
// of the pool. Rates are percentage points of one account's weekly quota per
// hour, the unit of the burn strip; the pooled line is the mean percentage left.

const (
	outlookWindow  = 7 * 24 * time.Hour
	outlookHorizon = 7 * 24 * time.Hour
	// Quota that refills unused counts as wasted pace when the pool loses at
	// least this share of itself (pool percentage points) within the horizon.
	outlookSlowPoolPct = 10.0
)

type replayAccount struct {
	ID      string
	Used    float64
	Limit   float64
	ResetAt time.Time
	Held    bool
	order   int
}

type outlookPoint struct {
	At          time.Time `json:"at"`
	LeftPercent float64   `json:"left_percent"`
}

type outlookRefill struct {
	At          time.Time `json:"at"`
	AccountID   string    `json:"account_id"`
	FromPercent float64   `json:"from_percent"`
	ToPercent   float64   `json:"to_percent"`
}

type outlookSpan struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type outlookLine struct {
	Points  []outlookPoint  `json:"points"`
	Refills []outlookRefill `json:"refills"`
	Empty   []outlookSpan   `json:"empty"`
}

// replayForecast is one account until its first refill in the horizon.
type replayForecast struct {
	FullAt   *time.Time
	RefillAt *time.Time
	Unused   *float64
}

type replayResult struct {
	Line     outlookLine
	Forecast map[string]replayForecast
	// Burn per account and UTC hour slot (index from the anchor hour), and the
	// slots in which demand found no usable account.
	Burn  map[int][]float64
	Short map[int]bool
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func hoursBetween(a, b time.Time) float64 { return b.Sub(a).Hours() }

func addHours(t time.Time, h float64) time.Time {
	return t.Add(time.Duration(h * float64(time.Hour)))
}

// rollResets moves readings whose reset already passed onto their next weekly
// refill: the vendor refilled the account, and its window recurs weekly.
func rollResets(accounts []replayAccount, start time.Time) {
	for i := range accounts {
		for !accounts[i].ResetAt.After(start) {
			accounts[i].ResetAt = accounts[i].ResetAt.Add(outlookWindow)
			accounts[i].Used = 0
		}
	}
}

func poolLeft(accounts []replayAccount) float64 {
	sum, n := 0.0, 0
	for _, a := range accounts {
		if !a.Held {
			sum += math.Max(0, 100-a.Used)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// replay runs the policy at rate from start until end. anchor is the UTC hour
// that slot 0 starts at; burn is binned per slot for the strip.
func replay(in []replayAccount, rate float64, start, end, anchor time.Time) replayResult {
	accounts := make([]replayAccount, len(in))
	copy(accounts, in)
	for i := range accounts {
		accounts[i].order = i
	}
	rollResets(accounts, start)
	out := replayResult{Line: outlookLine{Points: []outlookPoint{}, Refills: []outlookRefill{}, Empty: []outlookSpan{}}, Forecast: map[string]replayForecast{}, Burn: map[int][]float64{}, Short: map[int]bool{}}
	first := make([]time.Time, len(accounts))
	for i, a := range accounts {
		first[i] = a.ResetAt
		f := replayForecast{}
		if !a.Held && a.Used >= a.Limit-1e-9 {
			at := start
			f.FullAt = &at
		}
		out.Forecast[a.ID] = f
	}
	point := func(t time.Time) {
		v := round1(poolLeft(accounts))
		if n := len(out.Line.Points); n > 0 && out.Line.Points[n-1].At.Equal(t) && out.Line.Points[n-1].LeftPercent == v {
			return
		}
		out.Line.Points = append(out.Line.Points, outlookPoint{t, v})
	}
	bin := func(from, to time.Time, account int) {
		for t := from; t.Before(to); {
			slot := int(math.Floor(hoursBetween(anchor, t) + 1e-9))
			next := anchor.Add(time.Duration(slot+1) * time.Hour)
			if next.After(to) {
				next = to
			}
			if account < 0 {
				out.Short[slot] = true
			} else {
				if out.Burn[slot] == nil {
					out.Burn[slot] = make([]float64, len(accounts))
				}
				out.Burn[slot][account] += rate * hoursBetween(t, next)
			}
			t = next
		}
	}
	point(start)
	t := start
	var emptyFrom *time.Time
	for t.Before(end) {
		running := -1
		for i, a := range accounts {
			if a.Held || a.Used >= a.Limit-1e-9 {
				continue
			}
			if running < 0 || a.ResetAt.Before(accounts[running].ResetAt) || a.ResetAt.Equal(accounts[running].ResetAt) && a.order < accounts[running].order {
				running = i
			}
		}
		next, full := end, -1
		for _, a := range accounts {
			if !a.Held && a.ResetAt.Before(next) {
				next = a.ResetAt
			}
		}
		if running >= 0 && rate > 0 {
			if at := addHours(t, (accounts[running].Limit-accounts[running].Used)/rate); at.Before(next) {
				next, full = at, running
			}
		}
		if !next.After(t) {
			next = t.Add(time.Second)
		}
		if running >= 0 && rate > 0 {
			accounts[running].Used = math.Min(accounts[running].Limit, accounts[running].Used+rate*hoursBetween(t, next))
			bin(t, next, running)
		} else if rate > 0 {
			if emptyFrom == nil {
				at := t
				emptyFrom = &at
			}
			bin(t, next, -1)
		}
		t = next
		point(t)
		if full >= 0 {
			accounts[full].Used = accounts[full].Limit
			if f := out.Forecast[accounts[full].ID]; f.FullAt == nil && t.Before(first[full]) {
				at := t
				f.FullAt = &at
				out.Forecast[accounts[full].ID] = f
			}
		}
		// Refills at t, in a stable order; each is a vertical step in the line.
		due := []int{}
		for i, a := range accounts {
			if !a.ResetAt.After(t) {
				due = append(due, i)
			}
		}
		sort.SliceStable(due, func(x, y int) bool { return accounts[due[x]].order < accounts[due[y]].order })
		for _, i := range due {
			a := &accounts[i]
			if a.Held {
				a.ResetAt = a.ResetAt.Add(outlookWindow)
				a.Used = 0
				continue
			}
			if f := out.Forecast[a.ID]; f.RefillAt == nil {
				at, unused := a.ResetAt, math.Max(0, round1(a.Limit-a.Used))
				f.RefillAt, f.Unused = &at, &unused
				out.Forecast[a.ID] = f
			}
			from := round1(poolLeft(accounts))
			a.Used = 0
			at := a.ResetAt
			a.ResetAt = a.ResetAt.Add(outlookWindow)
			out.Line.Refills = append(out.Line.Refills, outlookRefill{at, a.ID, from, round1(poolLeft(accounts))})
			if emptyFrom != nil {
				out.Line.Empty = append(out.Line.Empty, outlookSpan{*emptyFrom, at})
				emptyFrom = nil
			}
			point(at)
		}
	}
	if emptyFrom != nil && emptyFrom.Before(end) {
		out.Line.Empty = append(out.Line.Empty, outlookSpan{*emptyFrom, end})
	}
	out.Line.Points = cornersOnly(out.Line.Points)
	return out
}

// cornersOnly keeps the corners of the piecewise linear line; refills stay
// vertical steps.
func cornersOnly(points []outlookPoint) []outlookPoint {
	if len(points) < 3 {
		return points
	}
	out := []outlookPoint{points[0]}
	for i := 1; i < len(points)-1; i++ {
		a, b, c := out[len(out)-1], points[i], points[i+1]
		ab, bc := hoursBetween(a.At, b.At), hoursBetween(b.At, c.At)
		if ab < 1e-9 || bc < 1e-9 || math.Abs((b.LeftPercent-a.LeftPercent)/ab-(c.LeftPercent-b.LeftPercent)/bc) > 1e-6 {
			out = append(out, b)
		}
	}
	return append(out, points[len(points)-1])
}

// safeRate is the highest pace at which the pool never runs empty before the
// horizon ends. Higher demand only empties accounts earlier, so the search is
// monotone.
func safeRate(accounts []replayAccount, start, end, anchor time.Time) float64 {
	pooled := 0
	for _, a := range accounts {
		if !a.Held {
			pooled++
		}
	}
	if pooled == 0 {
		return 0
	}
	lo, hi := 0.0, 100*float64(pooled)
	for range 40 {
		mid := (lo + hi) / 2
		if len(replay(accounts, mid, start, end, anchor).Line.Empty) > 0 {
			hi = mid
		} else {
			lo = mid
		}
	}
	return math.Floor(lo*100) / 100
}
