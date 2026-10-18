// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/accountprivacy"
	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// AEON-1497: GET /api/agent-accounts/quota-outlook is the Agents dial's fuel
// gauge. Per harness it pools the person's own accounts from the AEON-1499
// hourly rollup: what is used and left, burn per hour, the projection at the
// 24 h pace and at the safe pace, empty stretches, the verdict and per-account
// forecasts. Missing readings stay missing; nothing is extrapolated backwards.

const (
	outlookMaxAccounts = 64
	outlookGroupMax    = 16
	// A pace needs at least this much measured span.
	outlookPaceMinHours = 1.0
)

type outlookForecast struct {
	Kind          string     `json:"kind"`
	At            *time.Time `json:"at"`
	RefillAt      *time.Time `json:"refill_at"`
	UnusedPercent *float64   `json:"unused_percent"`
}

type outlookAccount struct {
	AccountID         string           `json:"account_id"`
	Label             string           `json:"label"`
	State             string           `json:"state"`
	Pooled            bool             `json:"pooled"`
	UsedPercent       *float64         `json:"used_percent"`
	LimitPercent      *float64         `json:"limit_percent"`
	ResetsAt          *time.Time       `json:"resets_at"`
	ReadAt            *time.Time       `json:"read_at"`
	ReadingAgeSeconds *int64           `json:"reading_age_seconds"`
	Freshness         string           `json:"freshness"`
	Forecast          *outlookForecast `json:"forecast"`
}

type outlookPool struct {
	Accounts      int       `json:"accounts"`
	UsedPercent   float64   `json:"used_percent"`
	LeftPercent   float64   `json:"left_percent"`
	SampledAt     time.Time `json:"sampled_at"`
	SampledAgeSec int64     `json:"sampled_age_seconds"`
}

type outlookPace struct {
	PerHour24h    *float64 `json:"per_hour_24h"`
	PerHour6h     *float64 `json:"per_hour_6h"`
	SafePerHour   *float64 `json:"safe_per_hour"`
	MeasuredHours float64  `json:"measured_hours"`
}

type outlookHeadline struct {
	Kind          string     `json:"kind"`
	At            *time.Time `json:"at"`
	Until         *time.Time `json:"until"`
	AccountID     string     `json:"account_id"`
	UnusedPercent *float64   `json:"unused_percent"`
	FocusAt       *time.Time `json:"focus_at"`
}

type outlookHour struct {
	HourAt time.Time `json:"hour_at"`
	Known  bool      `json:"known"`
	Empty  bool      `json:"empty"`
	Burn   []float64 `json:"burn"`
}

type outlookPastRefill struct {
	At        time.Time `json:"at"`
	AccountID string    `json:"account_id"`
}

type outlookGroup struct {
	Harness     string              `json:"harness"`
	State       string              `json:"state"`
	Accounts    []outlookAccount    `json:"accounts"`
	Pool        *outlookPool        `json:"pool"`
	Pace        outlookPace         `json:"pace"`
	Verdict     string              `json:"verdict"`
	Headline    outlookHeadline     `json:"headline"`
	History     []outlookHour       `json:"history"`
	PastLine    []outlookPoint      `json:"past_line"`
	PastRefills []outlookPastRefill `json:"past_refills"`
	Current     *outlookLine        `json:"current"`
	Safe        *outlookLine        `json:"safe"`
	Projected   []outlookHour       `json:"projected"`
}

type quotaOutlook struct {
	GeneratedAt  time.Time      `json:"generated_at"`
	AnchorHour   time.Time      `json:"anchor_hour"`
	HistoryHours int            `json:"history_hours"`
	HorizonHours int            `json:"horizon_hours"`
	Truncated    bool           `json:"truncated"`
	Groups       []outlookGroup `json:"groups"`
}

// weeklySample is one measured weekly reading of the account's binding bucket.
type weeklySample struct {
	ReadAt  time.Time
	Used    float64
	ResetAt time.Time
}

// outlookInput is one owned account with what the projection needs.
type outlookInput struct {
	ID, Label, Harness string
	Private, Held      bool
	Limit              float64
	Samples            []weeklySample // ascending, the newest last
	Freshness          string
}

func (m *Module) quotaOutlook(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if len(r.URL.RawQuery) > 0 {
		writeErr(w, fail(400, "quota outlook takes no parameters"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var out quotaOutlook
	err := m.read(ctx, p.TenantID, func(tx pgx.Tx) error {
		if p.Kind != tenant.Person || authz.RequireTx(ctx, tx, p, "account.read", authz.Scope{}) != nil {
			return fail(403, "account read permission required")
		}
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='5s'`); err != nil {
			return err
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		inputs, truncated, err := loadOutlookInputs(ctx, tx, p, now)
		if err != nil {
			return err
		}
		out = buildOutlook(inputs, now)
		out.Truncated = truncated
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

// loadOutlookInputs reads the person's own accounts (the dial's daily-limit
// family), their owner-sharing policy, floors, holds and weekly readings.
func loadOutlookInputs(ctx context.Context, tx pgx.Tx, p tenant.Principal, now time.Time) ([]outlookInput, bool, error) {
	all, err := overviewAccounts(ctx, tx)
	if err != nil {
		return nil, false, err
	}
	ids := make([]string, len(all))
	for i, a := range all {
		ids[i] = a.ID
	}
	owned, err := canonicalOwnedIDs(ctx, tx, p.ID, ids)
	if err != nil {
		return nil, false, err
	}
	accounts := []Account{}
	for _, a := range all {
		if owned[a.ID] && agentplan.HarnessLabel(a.Harness) != "" {
			accounts = append(accounts, a)
		}
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Harness != accounts[j].Harness {
			return accounts[i].Harness < accounts[j].Harness
		}
		if accounts[i].Label != accounts[j].Label {
			return accounts[i].Label < accounts[j].Label
		}
		return accounts[i].ID < accounts[j].ID
	})
	truncated := false
	perGroup := map[string]int{}
	kept := accounts[:0]
	for _, a := range accounts {
		if len(kept) >= outlookMaxAccounts || perGroup[a.Harness] >= outlookGroupMax {
			truncated = true
			continue
		}
		perGroup[a.Harness]++
		kept = append(kept, a)
	}
	accounts = kept
	keptIDs := make([]string, len(accounts))
	for i, a := range accounts {
		keptIDs[i] = a.ID
	}
	privacy, err := accountprivacy.Load(ctx, tx, p, keptIDs)
	if err != nil {
		return nil, false, err
	}
	// Private accounts' quota is never read, only their presence.
	visible := []string{}
	for _, id := range keptIDs {
		if privacy[id] {
			visible = append(visible, id)
		}
	}
	latest := map[string][]quotaSample{}
	rows, err := tx.Query(ctx, `SELECT id::text, quota_latest, link_revision FROM agent_accounts WHERE id = ANY($1::uuid[]) AND quota_latest IS NOT NULL`, visible)
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		var id string
		var raw []byte
		var revision int64
		if err := rows.Scan(&id, &raw, &revision); err != nil {
			rows.Close()
			return nil, false, err
		}
		var snapshot quotaSnapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			rows.Close()
			return nil, false, err
		}
		if snapshot.BindingRevision == revision {
			latest[id] = snapshot.Samples
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	anchor := now.UTC().Truncate(time.Hour)
	from := anchor.Add(-outlookWindow - time.Hour)
	hourly := map[string][][]quotaSample{}
	limit := len(visible)*(int(outlookWindow/time.Hour)+2) + 1
	rows, err = tx.Query(ctx, `SELECT h.account_id::text, h.samples FROM account_quota_hourly h
	 JOIN agent_accounts a ON a.tenant_id=h.tenant_id AND a.id=h.account_id AND h.binding_revision=a.link_revision
	 WHERE h.account_id = ANY($1::uuid[]) AND h.hour_at >= $2 AND h.hour_at <= $3 ORDER BY h.account_id, h.hour_at LIMIT $4`, visible, from, anchor, limit)
	if err != nil {
		return nil, false, err
	}
	count := 0
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, false, err
		}
		var samples []quotaSample
		if err := json.Unmarshal(raw, &samples); err != nil {
			rows.Close()
			return nil, false, err
		}
		hourly[id] = append(hourly[id], samples)
		count++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if count >= limit {
		return nil, false, fail(503, "quota history exceeds its bound")
	}
	out := make([]outlookInput, 0, len(accounts))
	for _, a := range accounts {
		in := outlookInput{ID: a.ID, Label: a.Label, Harness: a.Harness, Private: !privacy[a.ID], Limit: 100, Freshness: "unknown"}
		if in.Private {
			out = append(out, in)
			continue
		}
		u, err := loadUsagePolicy(ctx, tx, a.ID)
		if err != nil {
			return nil, false, err
		}
		in.Limit = math.Max(0, math.Min(100, 100-float64(u.Floor)))
		schedule, err := routingSchedule(ctx, tx, a)
		if err != nil {
			return nil, false, err
		}
		in.Held = a.State != "available" || schedule.ActiveOverride(now) == "hold"
		in.Samples = weeklySeries(latest[a.ID], hourly[a.ID], now)
		if n := len(in.Samples); n > 0 {
			last := in.Samples[n-1]
			in.Freshness = quotaSample{WindowKind: "weekly", UsedPercent: last.Used, ReadAt: last.ReadAt, ResetsAt: last.ResetAt}.reading().Freshness(now)
		}
		out = append(out, in)
	}
	return out, truncated, nil
}

// weeklySeries picks the account's binding weekly bucket (the most used one
// that has not expired, as routing caps on it) and orders its readings.
func weeklySeries(latest []quotaSample, hours [][]quotaSample, now time.Time) []weeklySample {
	bucket, best := "", -1.0
	var newest time.Time
	for _, v := range latest {
		if v.WindowKind != "weekly" {
			continue
		}
		live := v.ResetsAt.After(now)
		score := v.UsedPercent
		if !live {
			score = -1
		}
		if bucket == "" || score > best || score == best && v.ReadAt.After(newest) {
			bucket, best, newest = v.Bucket, score, v.ReadAt
		}
	}
	// The latest snapshot is the source of truth for presence; hourly rows of
	// a previous binding are already filtered out by the query.
	if bucket == "" {
		return nil
	}
	seen := map[int64]bool{}
	out := []weeklySample{}
	add := func(v quotaSample) {
		if v.WindowKind != "weekly" || v.Bucket != bucket || v.ReadAt.After(now) || seen[v.ReadAt.UnixNano()] {
			return
		}
		seen[v.ReadAt.UnixNano()] = true
		out = append(out, weeklySample{v.ReadAt.UTC(), v.UsedPercent, v.ResetsAt.UTC()})
	}
	for _, samples := range hours {
		for _, v := range samples {
			add(v)
		}
	}
	for _, v := range latest {
		add(v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReadAt.Before(out[j].ReadAt) })
	return out
}

// usedAt is the measured weekly use at t: the last reading at or before t,
// zero once that reading's reset has passed. ok is false before any reading.
func usedAt(samples []weeklySample, t time.Time) (float64, bool) {
	i := sort.Search(len(samples), func(i int) bool { return samples[i].ReadAt.After(t) }) - 1
	if i < 0 {
		return 0, false
	}
	if !samples[i].ResetAt.After(t) {
		return 0, true
	}
	return samples[i].Used, true
}

// consumed is the measured use between two instants: increases between
// readings, and after a refill the next reading's use from zero.
func consumed(samples []weeklySample, from, to time.Time) (float64, bool) {
	prev, ok := usedAt(samples, from)
	var prevReset time.Time
	if i := sort.Search(len(samples), func(i int) bool { return samples[i].ReadAt.After(from) }) - 1; i >= 0 {
		prevReset = samples[i].ResetAt
	}
	total := 0.0
	for _, s := range samples {
		if !s.ReadAt.After(from) || s.ReadAt.After(to) {
			continue
		}
		if !ok {
			prev, ok, prevReset = s.Used, true, s.ResetAt
			continue
		}
		if !prevReset.IsZero() && !prevReset.After(s.ReadAt) {
			total += s.Used
		} else {
			total += math.Max(0, s.Used-prev)
		}
		prev, prevReset = s.Used, s.ResetAt
	}
	return total, ok
}

func ptrF(v float64) *float64 { return &v }

func ptrT(t time.Time) *time.Time { return &t }

func buildOutlook(inputs []outlookInput, now time.Time) quotaOutlook {
	anchor := now.UTC().Truncate(time.Hour)
	out := quotaOutlook{GeneratedAt: now.UTC(), AnchorHour: anchor, HistoryHours: int(outlookWindow / time.Hour), HorizonHours: int(outlookHorizon / time.Hour), Groups: []outlookGroup{}}
	byHarness := map[string][]outlookInput{}
	order := []string{}
	for _, in := range inputs {
		if _, ok := byHarness[in.Harness]; !ok {
			order = append(order, in.Harness)
		}
		byHarness[in.Harness] = append(byHarness[in.Harness], in)
	}
	sort.Strings(order)
	for _, h := range order {
		out.Groups = append(out.Groups, buildGroup(h, byHarness[h], now, anchor))
	}
	return out
}

func buildGroup(harness string, inputs []outlookInput, now, anchor time.Time) outlookGroup {
	g := outlookGroup{Harness: harness, State: "not_reported", Accounts: []outlookAccount{}, Verdict: "unknown", Headline: outlookHeadline{Kind: "not_reported"},
		History: []outlookHour{}, PastLine: []outlookPoint{}, PastRefills: []outlookPastRefill{}, Projected: []outlookHour{}}
	pooled, held := []int{}, 0
	for i, in := range inputs {
		a := outlookAccount{AccountID: in.ID, Label: in.Label, State: "not_reported", Freshness: in.Freshness}
		switch {
		case in.Private:
			a.State, a.Freshness = "private", "unknown"
		case len(in.Samples) > 0:
			last := in.Samples[len(in.Samples)-1]
			used, _ := usedAt(in.Samples, now)
			reset := last.ResetAt
			for !reset.After(now) {
				reset = reset.Add(outlookWindow)
			}
			age := int64(now.Sub(last.ReadAt) / time.Second)
			a.UsedPercent, a.LimitPercent, a.ResetsAt, a.ReadAt, a.ReadingAgeSeconds = ptrF(round1(used)), ptrF(in.Limit), ptrT(reset), ptrT(last.ReadAt), &age
			if in.Held {
				a.State = "held"
				held++
			} else {
				a.State, a.Pooled = "ready", true
				if used >= in.Limit-1e-9 {
					a.State = "full"
				}
				pooled = append(pooled, i)
			}
		}
		g.Accounts = append(g.Accounts, a)
	}
	if len(pooled) == 0 {
		if held > 0 {
			g.State, g.Headline.Kind = "held", "held"
		}
		return g
	}
	g.State = "measured"
	n := len(inputs)
	// The pool now, and when it was sampled.
	left, sampled := 0.0, time.Time{}
	for _, i := range pooled {
		left += 100 - *g.Accounts[i].UsedPercent
		if r := *g.Accounts[i].ReadAt; r.After(sampled) {
			sampled = r
		}
	}
	left /= float64(len(pooled))
	g.Pool = &outlookPool{Accounts: len(pooled), UsedPercent: round1(100 - left), LeftPercent: round1(left), SampledAt: sampled, SampledAgeSec: int64(now.Sub(sampled) / time.Second)}

	// History: per UTC hour slot, measured burn per account, and the pooled line.
	start := anchor.Add(-outlookWindow)
	knownFrom := start
	for _, i := range pooled {
		first := inputs[i].Samples[0].ReadAt
		if _, ok := usedAt(inputs[i].Samples, start); !ok && first.After(knownFrom) {
			knownFrom = first
		}
	}
	pool := func(t time.Time) (float64, bool) {
		sum := 0.0
		for _, i := range pooled {
			v, ok := usedAt(inputs[i].Samples, t)
			if !ok {
				return 0, false
			}
			sum += 100 - v
		}
		return round1(sum / float64(len(pooled))), true
	}
	for slot := -int(outlookWindow / time.Hour); slot <= 0; slot++ {
		from, to := anchor.Add(time.Duration(slot)*time.Hour), anchor.Add(time.Duration(slot+1)*time.Hour)
		if to.After(now) {
			to = now
		}
		hour := outlookHour{HourAt: anchor.Add(time.Duration(slot) * time.Hour), Burn: make([]float64, n)}
		hour.Known = !from.Before(knownFrom)
		if hour.Known {
			empty := true
			for _, i := range pooled {
				v, _ := consumed(inputs[i].Samples, from, to)
				hour.Burn[i] = math.Round(v*100) / 100
				if u, _ := usedAt(inputs[i].Samples, to); u < inputs[i].Limit-1e-9 {
					empty = false
				}
			}
			hour.Empty = empty
		}
		g.History = append(g.History, hour)
	}
	// The pooled line at each slot end and around each measured refill.
	times := []time.Time{}
	for slot := -int(outlookWindow / time.Hour); slot < 0; slot++ {
		times = append(times, anchor.Add(time.Duration(slot+1)*time.Hour))
	}
	refills := map[string][]time.Time{}
	for _, i := range pooled {
		for _, s := range inputs[i].Samples {
			r := s.ResetAt
			if r.Before(start) || r.After(now) {
				continue
			}
			dup := false
			for _, seen := range refills[inputs[i].ID] {
				if math.Abs(r.Sub(seen).Minutes()) < 30 {
					dup = true
				}
			}
			if !dup {
				refills[inputs[i].ID] = append(refills[inputs[i].ID], r)
				g.PastRefills = append(g.PastRefills, outlookPastRefill{r, inputs[i].ID})
				times = append(times, r.Add(-time.Second), r)
			}
		}
	}
	sort.Slice(g.PastRefills, func(a, b int) bool { return g.PastRefills[a].At.Before(g.PastRefills[b].At) })
	times = append(times, now)
	sort.Slice(times, func(a, b int) bool { return times[a].Before(times[b]) })
	for _, t := range times {
		if t.Before(knownFrom) || t.After(now) {
			continue
		}
		if v, ok := pool(t); ok {
			g.PastLine = append(g.PastLine, outlookPoint{t, v})
		}
	}
	g.PastLine = cornersOnly(g.PastLine)

	// Pace over the measured span, then the policy replay at it and at the safe pace.
	paceOver := func(hours float64) (*float64, float64) {
		from := now.Add(-time.Duration(hours * float64(time.Hour)))
		if from.Before(knownFrom) {
			from = knownFrom
		}
		span := hoursBetween(from, now)
		if span < outlookPaceMinHours {
			return nil, span
		}
		sum := 0.0
		for _, i := range pooled {
			v, _ := consumed(inputs[i].Samples, from, now)
			sum += v
		}
		return ptrF(round1(sum / span)), span
	}
	var span float64
	g.Pace.PerHour24h, span = paceOver(24)
	g.Pace.PerHour6h, _ = paceOver(6)
	g.Pace.MeasuredHours = math.Round(span*10) / 10
	accounts := make([]replayAccount, len(pooled))
	for k, i := range pooled {
		accounts[k] = replayAccount{ID: inputs[i].ID, Used: *g.Accounts[i].UsedPercent, Limit: inputs[i].Limit, ResetAt: *g.Accounts[i].ResetsAt}
	}
	// The account the policy runs now.
	if running := runningAccount(accounts); running >= 0 {
		for _, i := range pooled {
			if inputs[i].ID == accounts[running].ID {
				g.Accounts[i].State = "active"
			}
		}
	}
	end := now.Add(outlookHorizon)
	safe := safeRate(accounts, now, end, anchor)
	g.Pace.SafePerHour = ptrF(safe)
	safeRun := replay(accounts, safe, now, end, anchor)
	g.Safe = &safeRun.Line
	if g.Pace.PerHour24h == nil {
		g.Headline.Kind = "unknown"
		return g
	}
	cur := replay(accounts, *g.Pace.PerHour24h, now, end, anchor)
	g.Current = &cur.Line
	index := map[string]int{}
	for i, in := range inputs {
		index[in.ID] = i
	}
	for slot := 0; slot < int(outlookHorizon/time.Hour); slot++ {
		hour := outlookHour{HourAt: anchor.Add(time.Duration(slot) * time.Hour), Known: true, Empty: cur.Short[slot], Burn: make([]float64, n)}
		for k, v := range cur.Burn[slot] {
			hour.Burn[index[accounts[k].ID]] = math.Round(v*100) / 100
		}
		g.Projected = append(g.Projected, hour)
	}
	// Per-account forecasts until each first refill.
	var poolEmpty *time.Time
	if len(cur.Line.Empty) > 0 {
		poolEmpty = &cur.Line.Empty[0].From
	}
	unusedTotal, best := 0.0, -1
	for _, i := range pooled {
		f := cur.Forecast[inputs[i].ID]
		fc := &outlookForecast{Kind: "enough", RefillAt: f.RefillAt}
		switch {
		case f.FullAt != nil && !f.FullAt.After(now):
			fc.Kind, fc.At = "waits", f.FullAt
		case f.FullAt != nil:
			fc.Kind, fc.At = "used_up", f.FullAt
			if poolEmpty != nil && math.Abs(f.FullAt.Sub(*poolEmpty).Minutes()) < 1 {
				fc.Kind = "empties"
			}
		case f.Unused != nil && *f.Unused >= 0.5:
			fc.Kind, fc.UnusedPercent = "unused", f.Unused
		}
		if f.Unused != nil {
			unusedTotal += *f.Unused
			if fc.Kind == "unused" && (best < 0 || *f.Unused > *g.Accounts[best].Forecast.UnusedPercent) {
				best = i
			}
		}
		g.Accounts[i].Forecast = fc
	}
	switch {
	case len(cur.Line.Empty) > 0:
		span := cur.Line.Empty[0]
		g.Verdict = "too_fast"
		g.Headline = outlookHeadline{Kind: "runs_dry", At: ptrT(span.From), FocusAt: ptrT(span.To)}
		if span.To.Before(end) {
			g.Headline.Until = ptrT(span.To)
			for _, r := range cur.Line.Refills {
				if r.At.Equal(span.To) {
					g.Headline.AccountID = r.AccountID
					break
				}
			}
		}
	case unusedTotal/float64(len(pooled)) >= outlookSlowPoolPct && best >= 0:
		fc := g.Accounts[best].Forecast
		g.Verdict = "too_slow"
		g.Headline = outlookHeadline{Kind: "unused", At: fc.RefillAt, AccountID: g.Accounts[best].AccountID, UnusedPercent: ptrF(math.Round(*fc.UnusedPercent)), FocusAt: fc.RefillAt}
	default:
		g.Verdict = "on_pace"
		g.Headline = outlookHeadline{Kind: "enough"}
		if len(cur.Line.Refills) > 0 {
			r := cur.Line.Refills[0]
			g.Headline.At, g.Headline.AccountID, g.Headline.FocusAt = ptrT(r.At), r.AccountID, ptrT(r.At)
		}
	}
	return g
}

// runningAccount is the usable account with the soonest refill.
func runningAccount(accounts []replayAccount) int {
	running := -1
	for i, a := range accounts {
		if a.Held || a.Used >= a.Limit-1e-9 {
			continue
		}
		if running < 0 || a.ResetAt.Before(accounts[running].ResetAt) {
			running = i
		}
	}
	return running
}
