// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/jackc/pgx/v5"
)

type tokenSample struct {
	From, At time.Time
	Model    string
	Tokens   int64
}
type ownInterval struct {
	capacity.UseSample
	Window string
}
type capacityLearning struct {
	TokenSamples           []tokenSample
	RecentOwn              []ownInterval
	Windows                []capacity.LearnedWindow
	FirstSeen              time.Time
	LastOwn, PresenceUntil *time.Time
	Correction             *capacity.Correction
	Online                 []time.Time
	Hits                   []capacity.LimitSample
	Tokens, Cost           int64
	Runs                   int
	Plan                   string
	PlanSince              time.Time
}

func loadLearning(ctx context.Context, tx pgx.Tx, id string) (capacityLearning, error) {
	var l capacityLearning
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT state FROM account_capacity_learning WHERE account_id=$1`, id).Scan(&raw)
	if isNoRows(err) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	err = json.Unmarshal(raw, &l)
	return l, err
}
func saveLearning(ctx context.Context, tx pgx.Tx, id string, l capacityLearning) error {
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO account_capacity_learning(tenant_id,account_id,state) SELECT tenant_id,id,$2 FROM agent_accounts WHERE id=$1 ON CONFLICT(tenant_id,account_id) DO UPDATE SET state=EXCLUDED.state,updated_at=now()`, id, raw)
	return err
}
func (l *capacityLearning) window(v capacity.Reading) *capacity.LearnedWindow {
	for i := range l.Windows {
		w := &l.Windows[i]
		if w.Kind == v.WindowKind && w.Bucket == v.Bucket {
			if w.Plan != v.Plan || w.Minutes != v.WindowMinutes {
				*w = capacity.LearnedWindow{Kind: v.WindowKind, Bucket: v.Bucket, Plan: v.Plan, Minutes: v.WindowMinutes}
			}
			return w
		}
	}
	if len(l.Windows) >= 32 {
		l.Windows = l.Windows[1:]
	}
	l.Windows = append(l.Windows, capacity.LearnedWindow{Kind: v.WindowKind, Bucket: v.Bucket, Plan: v.Plan, Minutes: v.WindowMinutes})
	return &l.Windows[len(l.Windows)-1]
}
func (l capacityLearning) metric(v capacity.Reading, now time.Time, s capacity.Schedule, profile string) capacity.WindowLearning {
	for _, w := range l.Windows {
		if w.Kind == v.WindowKind && w.Bucket == v.Bucket && (v.Plan == "" || w.Plan == v.Plan) && w.Minutes == v.WindowMinutes {
			return w.Summarize(now, s, profile)
		}
	}
	return capacity.WindowLearning{}
}
func (l capacityLearning) summary(now time.Time, s capacity.Schedule) capacity.LearningSummary {
	out := capacity.LearningSummary{Windows: []capacity.WindowLearning{}, Tokens: l.Tokens, Cost: l.Cost, Runs: l.Runs, LimitHits: len(l.Hits)}
	for _, w := range l.Windows {
		out.Windows = append(out.Windows, w.Summarize(now, s, ""))
	}
	if l.PresenceUntil != nil && l.PresenceUntil.After(now) {
		out.PresenceUntil = l.PresenceUntil
	}
	// Absence of observations is not absence of use: require recent connectivity.
	recent := false
	for _, at := range l.Online {
		if now.Sub(at) <= 2*time.Hour {
			recent = true
		}
	}
	out.Away = l.LastOwn != nil && now.Sub(*l.LastOwn) >= 48*time.Hour && recent
	out.Hours = capacity.WorkHours(l.Windows, now, s)
	if l.Correction != nil && now.Sub(l.Correction.At) <= 7*24*time.Hour {
		out.Correction = l.Correction
	}
	loc, _ := time.LoadLocation(s.Timezone)
	if loc != nil {
		days := map[string]bool{}
		nights := map[string]bool{}
		for _, at := range l.Online {
			if at.After(now) || now.Sub(at) > 14*24*time.Hour {
				continue
			}
			d := at.In(loc)
			if d.Hour() >= 8 && d.Hour() < 22 {
				days[d.Format("2006-01-02")] = true
			} else {
				nights[d.Format("2006-01-02")] = true
			}
		}
		out.Sleeps = len(days) >= 5 && len(nights)*5 < len(days)
	}
	return out
}

// Called after inserting a new authoritative observation, under the account
// lock. Replay/out-of-order measurements never train a second interval.
func learnReading(ctx context.Context, tx pgx.Tx, a Account, v capacity.Reading, now time.Time) error {
	l, err := loadLearning(ctx, tx, a.ID)
	if err != nil {
		return err
	}
	if l.FirstSeen.IsZero() {
		l.FirstSeen = v.ReadAt
	}
	w := l.window(v)
	var prev capacity.Reading
	err = tx.QueryRow(ctx, `SELECT read_at,used_percent::float8,resets_at,window_minutes,plan FROM account_capacity_readings WHERE account_id=$1 AND window_kind=$2 AND bucket=$3 AND source<>'estimate' AND read_at<$4 ORDER BY read_at DESC,CASE source WHEN 'harness' THEN 0 ELSE 1 END LIMIT 1`, a.ID, v.WindowKind, v.Bucket, v.ReadAt).Scan(&prev.ReadAt, &prev.UsedPercent, &prev.ResetsAt, &prev.WindowMinutes, &prev.Plan)
	if err != nil && !isNoRows(err) {
		return err
	}
	if err == nil && prev.ResetsAt.Equal(v.ResetsAt) && prev.WindowMinutes == v.WindowMinutes && prev.Plan == v.Plan && v.UsedPercent >= prev.UsedPercent {
		var overlap bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE account_id=$1 AND COALESCE(started_at,created_at)<=$3 AND (ended_at IS NULL OR ended_at>=$2) AND (started_at IS NOT NULL OR status IN ('starting','running','waiting')))`, a.ID, prev.ReadAt, v.ReadAt).Scan(&overlap)
		if err != nil {
			return err
		}
		delta := v.UsedPercent - prev.UsedPercent
		w.ObserveUse(prev.ReadAt, v.ReadAt, delta, !overlap)
		if !overlap && delta > 0 && v.ReadAt.Sub(prev.ReadAt) <= 6*time.Hour {
			at := v.ReadAt
			l.LastOwn = &at
			l.RecentOwn = append(l.RecentOwn, ownInterval{UseSample: capacity.UseSample{At: v.ReadAt, Percent: delta, Hours: v.ReadAt.Sub(prev.ReadAt).Hours()}, Window: v.WindowKind + "/" + v.Bucket})
			if len(l.RecentOwn) > 128 {
				l.RecentOwn = l.RecentOwn[len(l.RecentOwn)-128:]
			}
			// One action can move several quota windows. Presence must not add
			// those percentages together and invent a full point of own use.
			recent := map[string]float64{}
			for _, sample := range l.RecentOwn {
				if sample.Hours > 0 && sample.At.After(v.ReadAt.Add(-15*time.Minute)) {
					recent[sample.Window] += sample.Percent * math.Min(1, sample.At.Sub(v.ReadAt.Add(-15*time.Minute)).Hours()/sample.Hours)
				}
			}
			for _, points := range recent {
				if points >= 1 {
					until := v.ReadAt.Add(30 * time.Minute)
					l.PresenceUntil = &until
				}
			}
		}
	}
	// A contradiction widens uncertainty once, while the measured value wins.
	var estimate, uncertainty float64
	err = tx.QueryRow(ctx, `SELECT used_percent::float8,plus_minus::float8 FROM account_capacity_readings WHERE account_id=$1 AND window_kind=$2 AND bucket=$3 AND resets_at=$4 AND source='estimate' AND read_at<=$5 AND read_at>$5-interval '6 hours' ORDER BY read_at DESC LIMIT 1`, a.ID, v.WindowKind, v.Bucket, v.ResetsAt, v.ReadAt).Scan(&estimate, &uncertainty)
	if err != nil && !isNoRows(err) {
		return err
	}
	if err == nil && math.Abs(v.UsedPercent-estimate) > math.Max(3, uncertainty) {
		w.Sigma = math.Max(w.Sigma, math.Abs(v.UsedPercent-estimate))
		l.Correction = &capacity.Correction{Points: v.UsedPercent - estimate, At: v.ReadAt}
	}
	if err := saveLearning(ctx, tx, a.ID, l); err != nil {
		return err
	}
	if v.RunID != "" && v.Phase == "end" {
		return learnRun(ctx, tx, a, v.RunID, now)
	}
	return nil
}

// Run pairs are restricted to the same reset, duration and plan. A competing
// managed run makes attribution ambiguous, so that pair does not train L1/L2.
func learnRun(ctx context.Context, tx pgx.Tx, a Account, runID string, now time.Time) error {
	l, err := loadLearning(ctx, tx, a.ID)
	if err != nil {
		return err
	}
	before, _ := json.Marshal(l)
	rows, err := tx.Query(ctx, `SELECT e.window_kind,e.bucket,e.plan,e.window_minutes,e.read_at,e.used_percent::float8-b.used_percent::float8,extract(epoch FROM e.read_at-b.read_at)::float8/3600,COALESCE(ar.model_profile_id::text,''),COALESCE(ar.effective_model,''),ar.input_tokens+ar.output_tokens
 FROM account_capacity_readings e
 JOIN agent_runs ar ON ar.tenant_id=e.tenant_id AND ar.id=e.run_id
 JOIN LATERAL (SELECT * FROM account_capacity_readings b WHERE b.account_id=e.account_id AND b.run_id=e.run_id AND b.phase='start' AND b.source<>'estimate' AND b.window_kind=e.window_kind AND b.bucket=e.bucket AND b.plan=e.plan AND b.window_minutes=e.window_minutes AND b.resets_at=e.resets_at AND b.read_at<e.read_at ORDER BY b.read_at LIMIT 1) b ON true
 WHERE e.account_id=$1 AND e.run_id=$2 AND e.phase='end' AND e.source<>'estimate' AND e.used_percent>=b.used_percent
 AND NOT EXISTS(SELECT 1 FROM agent_runs other WHERE other.account_id=$1 AND other.id<>e.run_id AND other.started_at<e.read_at AND (other.ended_at IS NULL OR other.ended_at>b.read_at))
 ORDER BY e.read_at`, a.ID, runID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v capacity.Reading
		var r capacity.RunSample
		r.ID = runID
		if err = rows.Scan(&v.WindowKind, &v.Bucket, &v.Plan, &v.WindowMinutes, &r.At, &r.Percent, &r.Hours, &r.Profile, &r.Model, &r.Tokens); err != nil {
			rows.Close()
			return err
		}
		oldPlan := false
		for _, w := range l.Windows {
			if w.Kind == v.WindowKind && w.Bucket == v.Bucket && (w.Plan != v.Plan || w.Minutes != v.WindowMinutes) {
				oldPlan = true
			}
		}
		if !oldPlan {
			l.window(v).ObserveRun(r)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err = learnConsumption(ctx, tx, a, &l, now); err != nil {
		return err
	}
	if a.Harness == "grok" || a.Harness == "cursor" || a.Harness == "pi" {
		if err := learnBlindRuns(ctx, tx, a, &l, now); err != nil {
			return err
		}
	}
	after, _ := json.Marshal(l)
	if bytes.Equal(before, after) {
		return nil
	}
	if err = saveLearning(ctx, tx, a.ID, l); err != nil {
		return err
	}
	// Only the server's typed evidence can create an enforceable estimate.
	if a.Harness == "grok" || a.Harness == "cursor" || a.Harness == "pi" {
		if estimate := capacity.BlindEstimate(l.Hits, float64(l.Tokens), float64(l.Cost), now); estimate != nil {
			estimate.Plan = a.Plan
			estimate.PlusMinus = math.Max(estimate.PlusMinus, l.window(*estimate).Sigma)
			return persistEstimate(ctx, tx, a, *estimate, now)
		}
	}
	return nil
}

// Re-aggregate counters in the current bounded observation horizon on settlement.
// Telemetry is monotonic; grouping stops per run makes retries idempotent.
func learnConsumption(ctx context.Context, tx pgx.Tx, a Account, l *capacityLearning, now time.Time) error {
	since := now.Add(-capacity.LearningHorizon)
	if l.Plan != a.Plan {
		l.Plan = a.Plan
		if l.Runs > 0 || len(l.Hits) > 0 {
			l.PlanSince = now
		}
	}
	if l.PlanSince.After(since) {
		since = l.PlanSince
	}
	rows, err := tx.Query(ctx, `SELECT ar.id::text,COALESCE(ar.ended_at,ar.started_at,ar.created_at),ar.input_tokens+ar.output_tokens,ar.cost_micros,min(t.at) FILTER(WHERE t.error_code='vendor_limit'),max(t.limit_resets_at) FILTER(WHERE t.error_code='vendor_limit'),COALESCE(max(t.limit_window) FILTER(WHERE t.error_code='vendor_limit'),'')
 FROM agent_runs ar LEFT JOIN run_telemetry t ON t.tenant_id=ar.tenant_id AND t.run_id=ar.id
 WHERE ar.account_id=$1 AND ar.created_at>=$2 AND ar.purpose='managed'
 GROUP BY ar.id,ar.tenant_id ORDER BY COALESCE(ar.ended_at,ar.started_at,ar.created_at) DESC,ar.id LIMIT 2000`, a.ID, since)
	if err != nil {
		return err
	}
	type run struct {
		id           string
		at           time.Time
		tokens, cost int64
		hit, reset   *time.Time
		window       string
	}
	runs := []run{}
	for rows.Next() {
		var r run
		if err = rows.Scan(&r.id, &r.at, &r.tokens, &r.cost, &r.hit, &r.reset, &r.window); err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Unmanaged sessions contribute consumption, but never managed-run count.
	// Their exact deltas are already fenced by the harness receipt sequence.
	for _, sample := range l.TokenSamples {
		if !sample.From.Before(since) && !sample.At.After(now) {
			runs = append(runs, run{at: sample.At, tokens: sample.Tokens})
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].at.Equal(runs[j].at) {
			return runs[i].id < runs[j].id
		}
		return runs[i].at.Before(runs[j].at)
	})
	l.Hits = nil
	l.Tokens = 0
	l.Cost = 0
	l.Runs = 0
	var boundary time.Time
	for _, r := range runs {
		if !boundary.IsZero() && !r.at.Before(boundary) {
			l.Tokens, l.Cost, l.Runs = 0, 0, 0
			boundary = time.Time{}
		}
		l.Tokens += r.tokens
		l.Cost += r.cost
		if r.id != "" {
			l.Runs++
		}
		if r.hit != nil {
			cost := float64(l.Cost)
			if len(l.TokenSamples) > 0 {
				cost = 0 // Token-only sessions make a dollar total incomplete.
			}
			l.Hits = append(l.Hits, capacity.LimitSample{At: *r.hit, Tokens: float64(l.Tokens), Cost: cost, Reset: r.reset, Window: r.window})
			if r.reset != nil {
				boundary = *r.reset
			} else {
				boundary = r.hit.Add(time.Nanosecond)
			}
		}
	}
	if len(l.Hits) > 0 && l.Hits[len(l.Hits)-1].Reset != nil && !boundary.IsZero() && !now.Before(boundary) {
		l.Tokens, l.Cost, l.Runs = 0, 0, 0
	}
	return nil
}

func persistEstimate(ctx context.Context, tx pgx.Tx, a Account, v capacity.Reading, now time.Time) error {
	if err := v.Validate(now); err != nil {
		return err
	}
	// Never allow an estimate to hide a fresh measured window or clear a denial.
	var measured bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_capacity_readings WHERE account_id=$1 AND source<>'estimate' AND resets_at>$2 AND read_at>=$2-interval '10 minutes')`, a.ID, now).Scan(&measured); err != nil {
		return err
	}
	if measured {
		return nil
	}
	evidence, _ := json.Marshal(v.Evidence)
	inserted, err := tx.Exec(ctx, `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source,plan,plus_minus,evidence) SELECT tenant_id,id,$2,$3,$4,$5,$6,$7,'estimate',$8,$9,$10 FROM agent_accounts WHERE id=$1 ON CONFLICT DO NOTHING`, a.ID, v.WindowKind, v.Bucket, v.WindowMinutes, v.UsedPercent, v.ResetsAt, v.ReadAt, v.Plan, v.PlusMinus, evidence)
	if err != nil || inserted.RowsAffected() == 0 {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE account_allowance_windows SET capacity_retired=true WHERE account_id=$1 AND capacity_kind=$2 AND capacity_bucket=$3 AND ends_at<>$4 AND capacity_source='estimate'`, a.ID, v.WindowKind, v.Bucket, v.ResetsAt)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model,burst_ratio,capacity_kind,capacity_bucket,capacity_read_at,capacity_allowed,capacity_source)
 SELECT tenant_id,id,$2,$3,'percent',100,$4,'unrestricted',0,$5,$6,$7,true,'estimate' FROM agent_accounts WHERE id=$1
 ON CONFLICT(tenant_id,account_id,capacity_kind,capacity_bucket,ends_at) WHERE capacity_kind IS NOT NULL
 DO UPDATE SET used=EXCLUDED.used,capacity_read_at=EXCLUDED.capacity_read_at,capacity_retired=false,capacity_source='estimate' `, a.ID, v.StartsAt(), v.ResetsAt, int64(math.Ceil(math.Min(100, v.UsedPercent+v.PlusMinus))), v.WindowKind, v.Bucket, v.ReadAt)
	return err
}

func learnOnline(ctx context.Context, tx pgx.Tx, a Account, now time.Time) error {
	l, err := loadLearning(ctx, tx, a.ID)
	if err != nil {
		return err
	}
	at := now.Truncate(time.Hour)
	if len(l.Online) == 0 || l.Online[len(l.Online)-1] != at {
		l.Online = append(l.Online, at)
	}
	sort.Slice(l.Online, func(i, j int) bool { return l.Online[i].Before(l.Online[j]) })
	if len(l.Online) > 336 {
		l.Online = l.Online[len(l.Online)-336:]
	}
	return saveLearning(ctx, tx, a.ID, l)
}

// ObserveSessionTokens receives already-validated, monotonic unmanaged usage.
// Only the registering principal can affect this account's learning; managed
// sessions are excluded by the caller to avoid counting telemetry twice.
func ObserveSessionTokens(ctx context.Context, tx pgx.Tx, actorID, accountID, model string, delta int64, from, at time.Time) error {
	if delta <= 0 || !at.After(from) {
		return nil
	}
	a, err := lockAccount(ctx, tx, accountID)
	if err != nil {
		return err
	}
	if a.RegisteredBy != actorID {
		return nil
	}
	l, err := loadLearning(ctx, tx, a.ID)
	if err != nil {
		return err
	}
	l.TokenSamples = append(l.TokenSamples, tokenSample{From: from, At: at, Model: model, Tokens: delta})
	if len(l.TokenSamples) > 4096 {
		l.TokenSamples = l.TokenSamples[len(l.TokenSamples)-4096:]
	}
	if a.Harness == "grok" || a.Harness == "cursor" || a.Harness == "pi" {
		if err := learnConsumption(ctx, tx, a, &l, at); err != nil {
			return err
		}
		if err := saveLearning(ctx, tx, a.ID, l); err != nil {
			return err
		}
		if v := capacity.BlindEstimate(l.Hits, float64(l.Tokens), float64(l.Cost), at); v != nil {
			v.Plan = a.Plan
			v.PlusMinus = math.Max(v.PlusMinus, l.window(*v).Sigma)
			return persistEstimate(ctx, tx, a, *v, at)
		}
		return nil
	}
	if err = saveLearning(ctx, tx, a.ID, l); err != nil {
		return err
	}
	readings, err := readCapacity(ctx, tx, a.ID, true)
	if err != nil {
		return err
	}
	for _, v := range readings {
		// Locate the last measured baseline, even when a previous token estimate is
		// now the active projection. Never add a cumulative counter to itself.
		err := tx.QueryRow(ctx, `SELECT read_at,used_percent::float8 FROM account_capacity_readings WHERE account_id=$1 AND window_kind=$2 AND bucket=$3 AND resets_at=$4 AND source<>'estimate' ORDER BY read_at DESC LIMIT 1`, a.ID, v.WindowKind, v.Bucket, v.ResetsAt).Scan(&v.ReadAt, &v.UsedPercent)
		if isNoRows(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !at.Before(v.ResetsAt) {
			continue
		}
		total, n := 0.0, 0
		for _, w := range l.Windows {
			if w.Kind != v.WindowKind || w.Bucket != v.Bucket || w.Plan != v.Plan || w.Minutes != v.WindowMinutes {
				continue
			}
			for _, sample := range l.TokenSamples {
				if sample.From.Before(v.ReadAt) || sample.At.After(at) {
					continue
				}
				// L2 uses exact model evidence, never a vendor-wide token conversion.
				matching := w
				matching.Runs = nil
				for _, r := range w.Runs {
					if r.Model == sample.Model {
						matching.Runs = append(matching.Runs, r)
					}
				}
				metric := matching.Summarize(at, capacity.DefaultSchedule(), "")
				if metric.PerMillion > 0 {
					total += float64(sample.Tokens) * metric.PerMillion / 1e6
					n = metric.RunCount
				}
			}
		}
		if total <= 0 || n < 3 {
			continue
		}
		v.Source = "estimate"
		v.ReadAt = at
		v.RunID = ""
		v.Phase = ""
		v.OrdinaryUsageAllowed = nil
		v.UsedPercent = math.Min(100, v.UsedPercent+total)
		v.PlusMinus = math.Max(3, total*.25)
		v.PlusMinus = math.Max(v.PlusMinus, l.window(v).Sigma)
		v.Evidence = &capacity.Evidence{Kind: "tokens", Samples: n}
		if err = persistEstimate(ctx, tx, a, v, at); err != nil {
			return err
		}
	}
	return nil
}

func learnBlindRuns(ctx context.Context, tx pgx.Tx, a Account, l *capacityLearning, now time.Time) error {
	reading := capacity.BlindEstimate(l.Hits, 0, 0, now)
	if reading == nil {
		return nil
	}
	reading.Plan = a.Plan
	rows, err := tx.Query(ctx, `SELECT id::text,COALESCE(model_profile_id::text,''),COALESCE(effective_model,''),ended_at,extract(epoch FROM ended_at-started_at)::float8/3600,input_tokens+output_tokens,cost_micros FROM agent_runs WHERE account_id=$1 AND purpose='managed' AND status IN ('completed','failed') AND ended_at>started_at AND ended_at>=$2 ORDER BY ended_at DESC LIMIT 256`, a.ID, now.Add(-capacity.LearningHorizon))
	if err != nil {
		return err
	}
	defer rows.Close()
	var samples []capacity.RunSample
	for rows.Next() {
		var r capacity.RunSample
		var cost float64
		if err := rows.Scan(&r.ID, &r.Profile, &r.Model, &r.At, &r.Hours, &r.Tokens, &cost); err != nil {
			return err
		}
		if !l.PlanSince.IsZero() && r.At.Before(l.PlanSince) {
			continue
		}
		estimate := capacity.BlindEstimate(l.Hits, r.Tokens, cost, now)
		if estimate == nil {
			continue
		}
		r.Percent = estimate.UsedPercent
		samples = append(samples, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	w := l.window(*reading)
	for i := len(samples) - 1; i >= 0; i-- {
		w.ObserveRun(samples[i])
	}
	return nil
}
