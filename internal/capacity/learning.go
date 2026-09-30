// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"math"
	"sort"
	"time"
)

const LearningHorizon = 56 * 24 * time.Hour

// Evidence deliberately cannot contain arbitrary vendor text, paths or identities.
type Evidence struct {
	Kind    string `json:"kind"`
	Samples int    `json:"samples"`
}

type RunSample struct {
	ID                     string
	Profile                string
	Model                  string
	At                     time.Time
	Percent, Hours, Tokens float64
}
type UseSample struct {
	At             time.Time
	Percent, Hours float64
}
type Correction struct {
	Points float64   `json:"points"`
	At     time.Time `json:"at"`
}
type LearnedWindow struct {
	Kind, Bucket, Plan string
	Minutes            int
	Runs               []RunSample
	Own, Burn          []UseSample
	Sigma              float64
}

type WindowLearning struct {
	Kind        string  `json:"window_kind"`
	Bucket      string  `json:"bucket"`
	RunCount    int     `json:"run_count"`
	RunPercent  float64 `json:"run_percent,omitempty"`
	HoldPercent float64 `json:"hold_percent,omitempty"`
	PlusMinus   float64 `json:"plus_minus,omitempty"`
	PerMillion  float64 `json:"percent_per_million_tokens,omitempty"`
	PerHour     float64 `json:"percent_per_hour,omitempty"`
	AutoReserve float64 `json:"auto_reserve_percent,omitempty"`
	WorkDays    int     `json:"work_days"`
	BurnRate    float64 `json:"-"`
}
type SuggestedHours struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Days  int     `json:"days"`
}
type LearningSummary struct {
	Windows       []WindowLearning `json:"windows"`
	Tokens        int64            `json:"tokens"`
	Cost          int64            `json:"cost_micros"`
	Runs          int              `json:"runs"`
	LimitHits     int              `json:"limit_hits"`
	PresenceUntil *time.Time       `json:"presence_until,omitempty"`
	Away          bool             `json:"away_suggested,omitempty"`
	Sleeps        bool             `json:"sleeps_at_night,omitempty"`
	Hours         *SuggestedHours  `json:"suggested_hours,omitempty"`
	Correction    *Correction      `json:"correction,omitempty"`
}

type weighted struct{ value, weight float64 }

func quantile(values []weighted, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i].value < values[j].value })
	total := 0.0
	for _, v := range values {
		total += v.weight
	}
	sum := 0.0
	for _, v := range values {
		sum += v.weight
		if sum >= q*total {
			return v.value
		}
	}
	return values[len(values)-1].value
}
func weightAt(at, now time.Time) float64 {
	return math.Exp2(-math.Max(0, now.Sub(at).Hours()) / (14 * 24))
}
func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// TokenRate is per exact model within this plan/window. Profiles can choose
// different run sizes, but that must not split evidence for a token conversion.
func (w LearnedWindow) TokenRate(model string, now time.Time) (float64, int) {
	values := []weighted{}
	for _, r := range w.Runs {
		if r.Model == model && r.Tokens > 0 && !r.At.After(now) && now.Sub(r.At) <= LearningHorizon {
			values = append(values, weighted{r.Percent * 1e6 / r.Tokens, weightAt(r.At, now)})
		}
	}
	if len(values) < 3 {
		return 0, len(values)
	}
	return quantile(values, .75), len(values)
}

// Summarize uses exact plan/window/profile cohorts. Empty profile is advice for
// any profile and takes the largest learned hold, never a cheap mixed average.
func (w LearnedWindow) Summarize(now time.Time, s Schedule, profile string) WindowLearning {
	out := WindowLearning{Kind: w.Kind, Bucket: w.Bucket}
	cohorts := map[string][]RunSample{}
	for _, r := range w.Runs {
		if r.At.After(now) || now.Sub(r.At) > LearningHorizon || profile != "" && r.Profile != profile {
			continue
		}
		cohorts[r.Profile+"\x00"+r.Model] = append(cohorts[r.Profile+"\x00"+r.Model], r)
	}
	for _, runs := range cohorts {
		if len(runs) < 3 {
			continue
		}
		costs, rates, tokens := []weighted{}, []weighted{}, []weighted{}
		for _, r := range runs {
			wt := weightAt(r.At, now)
			costs = append(costs, weighted{r.Percent, wt})
			if r.Hours > 0 {
				rates = append(rates, weighted{r.Percent / r.Hours, wt})
			}
			if r.Tokens > 0 {
				tokens = append(tokens, weighted{r.Percent * 1e6 / r.Tokens, wt})
			}
		}
		hold := clamp(quantile(costs, .75), 1, 25)
		if hold > out.HoldPercent {
			out.RunCount = len(runs)
			out.HoldPercent = hold
			out.RunPercent = quantile(costs, .5)
			out.PlusMinus = math.Max(w.Sigma, (quantile(costs, .75)-quantile(costs, .25))/2)
			out.PerMillion = quantile(tokens, .75)
			out.PerHour = quantile(rates, .5)
		}
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return out
	}
	type dayUse struct {
		pct, hours float64
		at         time.Time
	}
	days := map[string]dayUse{}
	for _, v := range w.Own {
		if now.Sub(v.At) > LearningHorizon || v.At.After(now) || !s.WorkingAt(v.At) {
			continue
		}
		local := v.At.In(loc)
		key := local.Format("2006-01-02")
		d := days[key]
		d.pct += v.Percent
		d.hours += v.Hours
		d.at = v.At
		days[key] = d
	}
	own := []weighted{}
	for _, d := range days {
		if d.hours <= 0 {
			continue
		}
		ref := math.Min(float64(w.Minutes)/60, s.bandHours())
		own = append(own, weighted{d.pct / d.hours * ref, weightAt(d.at, now)})
	}
	out.WorkDays = len(own)
	if len(own) >= 5 {
		out.AutoReserve = clamp(quantile(own, .75), 10, 60)
	}
	// Use the last six hours of observed consumption, never a fabricated rate.
	burn, hours := 0.0, 0.0
	for _, v := range w.Burn {
		if !v.At.After(now) && now.Sub(v.At) <= 6*time.Hour {
			burn += v.Percent
			hours += v.Hours
		}
	}
	if hours > 0 {
		out.BurnRate = burn / hours
	}
	return out
}

// ObserveUse is fed only non-overlapping measured intervals in the same reset,
// plan and duration. own is false if any managed run overlaps the interval.
func (w *LearnedWindow) ObserveUse(from, to time.Time, delta float64, own bool) {
	if !to.After(from) || delta < 0 || delta > 100 || to.Sub(from) > 6*time.Hour {
		return
	}
	for at := from; at.Before(to); {
		end := at.Truncate(time.Hour).Add(time.Hour)
		if end.After(to) {
			end = to
		}
		fraction := end.Sub(at).Seconds() / to.Sub(from).Seconds()
		sample := UseSample{At: at.Add(end.Sub(at) / 2), Percent: delta * fraction, Hours: end.Sub(at).Hours()}
		w.Burn = appendUse(w.Burn, sample, 128)
		if own {
			w.Own = appendUse(w.Own, sample, 1400)
		}
		at = end
	}
}
func appendUse(samples []UseSample, v UseSample, limit int) []UseSample {
	for i := len(samples) - 1; i >= 0; i-- {
		if samples[i].At.Truncate(time.Hour) == v.At.Truncate(time.Hour) {
			samples[i].Percent += v.Percent
			samples[i].Hours += v.Hours
			return samples
		}
	}
	samples = append(samples, v)
	if len(samples) > limit {
		samples = samples[len(samples)-limit:]
	}
	return samples
}
func (w *LearnedWindow) ObserveRun(r RunSample) {
	if r.Percent < 0 || r.Percent > 100 || r.Hours <= 0 {
		return
	}
	for i := range w.Runs {
		if w.Runs[i].ID == r.ID {
			w.Runs[i] = r
			return
		}
	}
	w.Runs = append(w.Runs, r)
	if len(w.Runs) > 256 {
		w.Runs = w.Runs[len(w.Runs)-256:]
	}
}

// WorkHours requires five observed days and uses the central 80% of own use.
// It suggests only; the caller must save a schedule explicitly.
func WorkHours(windows []LearnedWindow, now time.Time, s Schedule) *SuggestedHours {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return nil
	}
	hours := []weighted{}
	days := map[string]bool{}
	// Different simultaneous quota windows measure the same activity: use the
	// most-observed window, rather than multiply the user's use by window count.
	var own []UseSample
	for _, w := range windows {
		if len(w.Own) > len(own) {
			own = w.Own
		}
	}
	for _, v := range own {
		if v.Percent <= 0 || v.At.After(now) || now.Sub(v.At) > 14*24*time.Hour {
			continue
		}
		d := v.At.In(loc)
		days[d.Format("2006-01-02")] = true
		hours = append(hours, weighted{float64(d.Hour()) + float64(d.Minute())/60, v.Percent})
	}
	if len(days) < 5 {
		return nil
	}
	start := math.Floor(quantile(hours, .1)*2) / 2
	end := math.Min(24, math.Ceil((quantile(hours, .9)+.5)*2)/2)
	if end <= start {
		return nil
	}
	same := true
	for _, d := range s.Week {
		if d.On && (d.Start != start || d.End != end) {
			same = false
		}
	}
	if same {
		return nil
	}
	return &SuggestedHours{Start: start, End: end, Days: len(days)}
}

type LimitSample struct {
	At           time.Time
	Tokens, Cost float64
	Reset        *time.Time
	Window       string
}

// BlindEstimate needs an observed reset interval (or two observed stop cycles),
// never an assumed vendor allowance. The first partial cycle has wide uncertainty.
func BlindEstimate(hits []LimitSample, tokens, cost float64, now time.Time) *Reading {
	if len(hits) == 0 {
		return nil
	}
	last := hits[len(hits)-1]
	durations := []weighted{}
	levels := []weighted{}
	useCost := true
	for _, h := range hits {
		if h.Cost <= 0 {
			useCost = false
		}
	}
	for i, h := range hits {
		// A named fixed-length vendor window calibrates on its first stop.
		// Monthly/other windows need an observed cycle; don't assume 30 days.
		if h.Reset != nil {
			switch h.Window {
			case "5h":
				durations = append(durations, weighted{300, 1})
			case "weekly":
				durations = append(durations, weighted{10080, 1})
			}
		}
		value := h.Tokens
		if useCost {
			value = h.Cost
		}
		if value > 0 {
			levels = append(levels, weighted{value, 1})
		}
		if i > 0 {
			prev := hits[i-1]
			if prev.Reset != nil && h.Reset != nil {
				if d := h.Reset.Sub(*prev.Reset).Minutes(); d >= 1 && d <= 527040 {
					durations = append(durations, weighted{d, 1})
				}
			} else if d := h.At.Sub(prev.At).Minutes(); d >= 1 && d <= 527040 {
				durations = append(durations, weighted{d, 1})
			}
		}
	}
	if len(levels) == 0 || len(durations) == 0 {
		return nil
	}
	minutes := int(math.Ceil(quantile(durations, .5)))
	reset := last.At.Add(time.Duration(minutes) * time.Minute)
	if last.Reset != nil {
		reset = *last.Reset
	}
	// An expired estimate cannot mint a new cycle. A later run/probe supplies
	// fresh consumption and a new estimate anchored to the observed cycle.
	for !reset.After(now) {
		reset = reset.Add(time.Duration(minutes) * time.Minute)
	}
	used := tokens
	if useCost {
		used = cost
	}
	sort.Slice(levels, func(i, j int) bool { return levels[i].value < levels[j].value })
	median := levels[len(levels)/2].value
	if len(levels)%2 == 0 {
		median = (median + levels[len(levels)/2-1].value) / 2
	}
	spread := 100 * (quantile(levels, .75) - quantile(levels, .25)) / median
	sigma := math.Max(10, math.Max(20/math.Sqrt(float64(len(levels))), spread))
	return &Reading{WindowKind: "other", Bucket: "learned", WindowMinutes: minutes, UsedPercent: clamp(100*used/median, 0, 100), ResetsAt: reset, ReadAt: now, Source: "estimate", PlusMinus: math.Min(100, sigma), Evidence: &Evidence{Kind: "limit_hits", Samples: len(levels)}}
}
