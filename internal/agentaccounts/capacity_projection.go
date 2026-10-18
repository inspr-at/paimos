// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/jackc/pgx/v5"
)

// capacityProjection is a bounded, transaction-local read model. The wrapper
// exposes typed inputs to the existing admission helpers, so projections use
// exactly the mutation path's maths without querying once per account/window.
// It is constructed only by projectCapacity, never by reservation or claim.
type capacityProjection struct {
	accounts    map[string]Account
	learning    map[string]capacityLearning
	policies    map[string]usagePolicy
	schedules   map[string]capacity.Schedule
	entries     map[string][]scheduleEntry
	defaults    map[string]capacity.Schedule
	readings    map[string][]capacity.Reading
	stats       map[string]capacityStats
	limits      map[string]*LimitRule
	estimates   map[string]map[string]int64
	limitUse    map[string]float64
	fromWindows map[string]Window
	spend       map[string]string
	shared      map[string][]Window
	facts       map[string][]ReadinessFact
	legacy      map[string][]ReadinessFact
	vendor      map[string]*ReadinessFact
	named       map[string]*time.Time
	local       map[string]string
	resources   map[string]string
	early       map[string]*string
	busy        map[string]bool
	factLedgers map[string]capacityFactLedger
	hosts       map[string]agentpairing.AccountHostCapacity
	baselines   map[string]capacityBaseline
}
type capacityStats struct {
	cost, approved, allowed, unconfirmed bool
	failure                              string
	reset                                *time.Time
}
type capacityBaseline struct {
	used  float64
	at    time.Time
	found bool
}
type capacityReadTx struct {
	pgx.Tx
	projection *capacityProjection
}

func projectionInputs(tx pgx.Tx) *capacityProjection {
	if read, ok := tx.(*capacityReadTx); ok {
		return read.projection
	}
	return nil
}

func baselineKey(id string, v capacity.Reading, bound time.Time) string {
	return id + "/" + v.WindowKind + "/" + v.Bucket + "/" + v.ResetsAt.UTC().Format(time.RFC3339Nano) + "/" + v.ReadAt.UTC().Format(time.RFC3339Nano) + "/" + bound.UTC().Format(time.RFC3339Nano)
}
func readPacingBaseline(ctx context.Context, tx pgx.Tx, id string, v capacity.Reading, bound time.Time) (float64, time.Time, error) {
	if inputs := projectionInputs(tx); inputs != nil {
		b := inputs.baselines[baselineKey(id, v, bound)]
		if !b.found {
			return 0, time.Time{}, pgx.ErrNoRows
		}
		return b.used, b.at, nil
	}
	var used float64
	var at time.Time
	err := tx.QueryRow(ctx, capacityBaselineSQL, id, v.WindowKind, v.Bucket, v.ResetsAt, bound, v.ReadAt).Scan(&used, &at)
	return used, at, err
}

type capacityBaselineInput struct {
	ID     string    `json:"id"`
	Key    string    `json:"key"`
	Kind   string    `json:"kind"`
	Bucket string    `json:"bucket"`
	Reset  time.Time `json:"reset"`
	Start  time.Time `json:"start"`
	At     time.Time `json:"at"`
}

func (s *capacityProjection) loadProjectionBaselines(ctx context.Context, tx pgx.Tx, accounts []Account, person string, draft *previewDraft, now time.Time) error {
	inputs := []capacityBaselineInput{}
	seen := map[string]bool{}
	add := func(id string, v capacity.Reading, schedule capacity.Schedule) error {
		if v.WindowKind == "fact" {
			return nil
		}
		period, _ := schedule.Period(now)
		bound := period
		if start := v.StartsAt(); !start.Before(period) {
			bound = start
		}
		key := baselineKey(id, v, bound)
		if seen[key] {
			return nil
		}
		seen[key] = true
		if len(inputs) == maxWaitWindows {
			return fail(503, "capacity baseline snapshot exceeds bound")
		}
		inputs = append(inputs, capacityBaselineInput{id, key, v.WindowKind, v.Bucket, v.ResetsAt, bound, v.ReadAt})
		return nil
	}
	spends := []struct {
		ID    string    `json:"id"`
		Start time.Time `json:"start"`
	}{}
	entries, fallback := s.entries[person], s.defaults[person]
	previous := userSchedule(entries, fallback)
	readTx := &capacityReadTx{Tx: tx, projection: s}
	for _, a := range accounts {
		schedule := resolveSchedule(entries, a, fallback, now)
		if draft != nil {
			schedule = scheduleWithDraft(entries, a, previous, draft.schedule, draft.pools, now)
		}
		start, _ := limitPeriod("month", schedule.Timezone, now)
		spends = append(spends, struct {
			ID    string    `json:"id"`
			Start time.Time `json:"start"`
		}{a.ID, start})
		for _, v := range s.readings[a.ID] {
			if err := add(a.ID, v, schedule); err != nil {
				return err
			}
		}
		if draft != nil {
			continue
		}
		windows := a.Windows
		if a.QuotaPoolFingerprint != "" {
			windows = s.shared[a.Harness+":"+a.QuotaPoolFingerprint]
		}
		facts, err := factWindows(ctx, readTx, a, now)
		if err != nil {
			return err
		}
		for _, w := range append(append([]Window{}, windows...), facts...) {
			if w.capacityReadAt == nil || synthetic(w) {
				continue
			}
			v := capacity.Reading{WindowKind: w.capacityKind, Bucket: w.capacityBucket, WindowMinutes: int(w.EndsAt.Sub(w.StartsAt) / time.Minute), ResetsAt: w.EndsAt, ReadAt: *w.capacityReadAt}
			if err := add(w.AccountID, v, s.schedules[a.ID]); err != nil {
				return err
			}
		}
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		return err
	}
	query := `SELECT batch.key,baseline.* FROM (` + strings.NewReplacer("$1", "batch.id", "$2", "batch.kind", "$3", "batch.bucket", "$4", "batch.reset", "$5", "batch.start", "$6", "batch.at").Replace(capacityBaselineSQL) + `) baseline`
	if err = capacityRows(ctx, tx, `jsonb_to_recordset($1::jsonb) batch(id uuid,key text,kind text,bucket text,reset timestamptz,start timestamptz,at timestamptz)`, query, []any{raw}, func(row pgx.Rows) error {
		var id, key string
		var b capacityBaseline
		if err := row.Scan(&id, &key, &b.used, &b.at); err != nil {
			return err
		}
		b.found = true
		s.baselines[key] = b
		return nil
	}); err != nil {
		return err
	}
	raw, err = json.Marshal(spends)
	if err != nil {
		return err
	}
	return capacityRows(ctx, tx, `jsonb_to_recordset($1::jsonb) batch(id uuid,start timestamptz)`, `SELECT bool_or(billing_mode='api'),round(COALESCE(sum(estimated_cost_usd) FILTER(WHERE billing_mode='api' AND reported_at>=batch.start),0),2)::text FROM harness_session_usage WHERE account_id=batch.id`, []any{raw}, func(row pgx.Rows) error {
		var id, value string
		var api *bool
		if err := row.Scan(&id, &api, &value); err != nil {
			return err
		}
		if api != nil && *api {
			s.spend[id] = value
		}
		return nil
	})
}

// capacityRows vectorizes the same typed SQL used by the single-account
// mutation readers. The lateral input relation supplies account/window keys;
// one statement returns the whole set, with an explicit materialization cap.
func capacityRows(ctx context.Context, tx pgx.Tx, from, query string, args []any, read func(pgx.Rows) error) error {
	rows, err := tx.Query(ctx, `SELECT batch.id::text,data.* FROM `+from+` CROSS JOIN LATERAL (`+query+`) data LIMIT 16385`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		if count == maxWaitWindows {
			return fail(503, "capacity input snapshot exceeds bound")
		}
		count++
		if err := read(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
func accountCapacityRows(ctx context.Context, tx pgx.Tx, ids []string, query string, args []any, read func(pgx.Rows) error) error {
	return capacityRows(ctx, tx, `unnest($1::uuid[]) batch(id)`, strings.ReplaceAll(query, "$1", "batch.id"), append([]any{ids}, args...), read)
}

// Capacity already bounds the complete account set. Read its context labels
// as one bounded set too, rather than the small batches used by other callers.
func capacityContextLabels(ctx context.Context, tx pgx.Tx, ids []string) (map[string][]accountuse.ContextLabel, error) {
	rows, err := tx.Query(ctx, `SELECT x.account_id::text,c.id::text,c.name FROM account_use_cells x JOIN work_contexts c ON c.tenant_id=x.tenant_id AND c.id=x.context_id WHERE x.account_id=ANY($1::uuid[]) AND c.archived_at IS NULL ORDER BY x.account_id,c.name,c.id LIMIT 16385`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]accountuse.ContextLabel{}
	n := 0
	for rows.Next() {
		if n == maxWaitWindows {
			return nil, fail(503, "capacity context snapshot exceeds bound")
		}
		n++
		var id string
		var label accountuse.ContextLabel
		if err := rows.Scan(&id, &label.ID, &label.Name); err != nil {
			return nil, err
		}
		out[id] = append(out[id], label)
	}
	return out, rows.Err()
}

func loadCapacityProjection(ctx context.Context, tx pgx.Tx, accounts []Account, person string, now time.Time) (*capacityProjection, error) {
	s := &capacityProjection{
		accounts:    map[string]Account{},
		learning:    map[string]capacityLearning{},
		policies:    map[string]usagePolicy{},
		schedules:   map[string]capacity.Schedule{},
		entries:     map[string][]scheduleEntry{},
		defaults:    map[string]capacity.Schedule{},
		readings:    map[string][]capacity.Reading{},
		stats:       map[string]capacityStats{},
		limits:      map[string]*LimitRule{},
		estimates:   map[string]map[string]int64{},
		limitUse:    map[string]float64{},
		fromWindows: map[string]Window{},
		spend:       map[string]string{},
		shared:      map[string][]Window{},
		facts:       map[string][]ReadinessFact{},
		legacy:      map[string][]ReadinessFact{},
		vendor:      map[string]*ReadinessFact{},
		named:       map[string]*time.Time{},
		local:       map[string]string{},
		resources:   map[string]string{},
		early:       map[string]*string{},
		busy:        map[string]bool{},
		baselines:   map[string]capacityBaseline{},
	}
	// Retired confirmed siblings can still hold quota, native roots or limits.
	// Include them in the input set, while the response retains its live list.
	activeIDs := make([]string, len(accounts))
	for i, a := range accounts {
		activeIDs[i] = a.ID
	}
	all, err := queryCapacityAccounts(ctx, tx, true, activeIDs)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(all))
	for i, a := range all {
		ids[i] = a.ID
		s.accounts[a.ID] = a
	}
	load := func(query string, args []any, read func(pgx.Rows) error) error {
		return accountCapacityRows(ctx, tx, ids, query, args, read)
	}
	if err = load(`SELECT state FROM account_capacity_learning WHERE account_id=$1`, nil, func(row pgx.Rows) error {
		var id string
		var raw []byte
		if err := row.Scan(&id, &raw); err != nil {
			return err
		}
		var l capacityLearning
		if err := json.Unmarshal(raw, &l); err != nil {
			return err
		}
		s.learning[id] = l
		return nil
	}); err != nil {
		return nil, err
	}
	if err = load(capacityUsageSQL(), []any{now}, func(row pgx.Rows) error {
		var id string
		var account, person, workspace *string
		var p usagePolicy
		if err := row.Scan(&id, &account, &person, &workspace, &p.OwnFloor, &p.Revision, &p.BindingRevision, &p.Floor, &p.Boost, &p.BoostUntil); err != nil {
			return err
		}
		p.AccountID, p.Posture, p.Source = id, "balanced", "schedule"
		var err error
		p, err = resolveUsagePolicy(p, account, person, workspace)
		s.policies[id] = p
		return err
	}); err != nil {
		return nil, err
	}
	if err = load(`SELECT
 EXISTS(SELECT 1 FROM run_telemetry t JOIN agent_runs r ON r.tenant_id=t.tenant_id AND r.id=t.run_id WHERE r.account_id=a.id AND t.cost_micros_delta>0),
 NOT EXISTS(SELECT 1 FROM agent_pairing_enrollments e JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id WHERE e.account_id=a.id AND (e.ongoing_approved_at IS NULL OR e.state<>'connected' OR c.state<>'connected' OR q.state<>'redeemed')),
 EXISTS(SELECT 1 FROM model_profiles WHERE enabled AND harness=a.harness AND aeon_account_allows_profile(a.harness,a.allowed_model_profile_ids,id)),
 EXISTS(SELECT 1 FROM agent_runs WHERE account_id=a.id AND trace->'work_lifecycle_release'->>'exit_unconfirmed'='true'),
 a.last_probe_failure,(SELECT min(w.ends_at) FROM account_allowance_windows w WHERE w.account_id=a.id AND w.capacity_read_at IS NOT NULL AND NOT w.capacity_retired AND w.ends_at>$2)
 FROM agent_accounts a WHERE a.id=$1`, []any{now}, func(row pgx.Rows) error {
		var id string
		var st capacityStats
		if err := row.Scan(&id, &st.cost, &st.approved, &st.allowed, &st.unconfirmed, &st.failure, &st.reset); err != nil {
			return err
		}
		s.stats[id] = st
		return nil
	}); err != nil {
		return nil, err
	}
	owners := map[string]string{}
	people := []string{person}
	seen := map[string]bool{person: true}
	ownerSQL := `SELECT ` + modelprefs.CanonicalPersonSQL("owner.person::uuid") + `::text FROM (` + capacityOwnerSQL + `) owner(person)`
	if err = load(ownerSQL, nil, func(row pgx.Rows) error {
		var id string
		var owner *string
		if err := row.Scan(&id, &owner); err != nil {
			return err
		}
		if owner != nil {
			owners[id] = *owner
			if !seen[*owner] {
				seen[*owner] = true
				people = append(people, *owner)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err = accountCapacityRows(ctx, tx, people, `SELECT scope,scope_key,schedule FROM account_capacity_schedules WHERE principal_id=$1 ORDER BY scope,scope_key`, nil, func(row pgx.Rows) error {
		var id string
		var e scheduleEntry
		var raw []byte
		if err := row.Scan(&id, &e.Scope, &e.Key, &raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &e.Schedule); err != nil {
			return err
		}
		s.entries[id] = append(s.entries[id], e)
		return nil
	}); err != nil {
		return nil, err
	}
	for _, id := range people {
		s.defaults[id] = capacity.DefaultSchedule()
	}
	if err = accountCapacityRows(ctx, tx, people, `SELECT timezone FROM personal_profiles WHERE principal_id=$1`, nil, func(row pgx.Rows) error {
		var id, zone string
		if err := row.Scan(&id, &zone); err != nil {
			return err
		}
		s.defaults[id] = capacity.DefaultSchedule(zone)
		return nil
	}); err != nil {
		return nil, err
	}
	for _, a := range all {
		schedule := capacity.DefaultSchedule()
		if owner := owners[a.ID]; owner != "" {
			schedule = resolveSchedule(s.entries[owner], a, s.defaults[owner], now)
		}
		s.schedules[a.ID] = usageSchedule(schedule, s.policies[a.ID], now)
	}
	if err = load(currentReadingsSQL, []any{now}, func(row pgx.Rows) error {
		var id string
		var v capacity.Reading
		if err := row.Scan(&id, &v.WindowKind, &v.Bucket, &v.WindowMinutes, &v.UsedPercent, &v.ResetsAt, &v.ReadAt, &v.Source, &v.Plan, &v.OrdinaryUsageAllowed, &v.RunID, &v.Phase, &v.PlusMinus, &v.Evidence); err != nil {
			return err
		}
		s.readings[id] = append(s.readings[id], v)
		return nil
	}); err != nil {
		return nil, err
	}
	windows, err := readAccountWindowsLimit(ctx, tx, ids, false, maxWaitWindows)
	if err != nil {
		return nil, err
	}
	pools := map[string][]string{}
	for _, a := range all {
		for _, w := range windows[a.ID] {
			s.fromWindows[w.ID] = w
		}
		if a.QuotaPoolFingerprint != "" {
			key := a.Harness + ":" + a.QuotaPoolFingerprint
			pools[key] = append(pools[key], a.ID)
		}
	}
	for key, members := range pools {
		s.shared[key] = projectQuotaWindows(members, windows, now)
	}
	s.hosts, err = agentpairing.HostCapacitiesForAccounts(ctx, tx, ids, now)
	if err != nil {
		return nil, err
	}
	if err = s.loadProjectionLimits(ctx, tx, ids, now); err != nil {
		return nil, err
	}
	if err = s.loadProjectionFacts(ctx, tx, ids, now); err != nil {
		return nil, err
	}
	return s, nil
}

const currentReadingsSQL = `SELECT window_kind,bucket,window_minutes,used_percent::float8,resets_at,read_at,source,plan,ordinary_usage_allowed,COALESCE(run_id::text,''),phase,plus_minus::float8,evidence FROM (SELECT DISTINCT ON(window_kind,bucket) * FROM account_capacity_readings WHERE account_id=$1 AND (EXISTS(SELECT 1 FROM account_allowance_windows w WHERE w.account_id=account_capacity_readings.account_id AND w.capacity_kind=window_kind AND w.capacity_bucket=bucket AND w.capacity_read_at=read_at AND NOT w.capacity_retired)) ORDER BY window_kind,bucket,CASE WHEN source<>'estimate' AND read_at>=$2::timestamptz-interval '10 minutes' AND resets_at>$2 THEN 0 ELSE 1 END,read_at DESC, CASE source WHEN 'harness' THEN 0 WHEN 'agentd' THEN 1 ELSE 2 END) r ORDER BY resets_at,window_kind,bucket`

type capacityPeriodInput struct {
	ID       string    `json:"id"`
	Start    time.Time `json:"start"`
	Now      time.Time `json:"now"`
	Unit     string    `json:"unit"`
	Estimate int64     `json:"estimate"`
}

func (s *capacityProjection) loadProjectionLimits(ctx context.Context, tx pgx.Tx, ids []string, now time.Time) error {
	load := func(q string, args []any, read func(pgx.Rows) error) error {
		return accountCapacityRows(ctx, tx, ids, q, args, read)
	}
	if err := load(`SELECT `+limitColumns+` FROM account_limit_rules WHERE account_id=$1 AND removed_at IS NULL`, nil, func(row pgx.Rows) error {
		var id string
		var r LimitRule
		if err := row.Scan(&id, &r.ID, &r.AccountID, &r.Amount, &r.Unit, &r.Period, &r.FromWindowID, &r.CreatedAt); err != nil {
			return err
		}
		s.limits[id] = &r
		return nil
	}); err != nil {
		return err
	}
	if err := load(capacityEstimatesSQL, nil, func(row pgx.Rows) error {
		var id string
		var requests, tokens, cost *int64
		if err := row.Scan(&id, &requests, &tokens, &cost); err != nil {
			return err
		}
		v := map[string]int64{"runs": 1, "percent": 1, "requests": 1, "tokens": 100000, "cost_micros": 1000000}
		for _, x := range []struct {
			key   string
			value *int64
		}{{"requests", requests}, {"tokens", tokens}, {"cost_micros", cost}} {
			if x.value != nil {
				v[x.key] = *x.value
			}
		}
		s.estimates[id] = v
		return nil
	}); err != nil {
		return err
	}
	inputs := []capacityPeriodInput{}
	for _, id := range ids {
		if rule := s.limits[id]; rule != nil {
			start, _ := limitPeriod(rule.Period, s.schedules[id].Timezone, now)
			inputs = append(inputs, capacityPeriodInput{id, start, now, rule.Unit, s.estimates[id][rule.Unit]})
		}
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		return err
	}
	from := `jsonb_to_recordset($1::jsonb) batch(id uuid,start timestamptz,now timestamptz,unit text,estimate bigint)`
	add := func(query, filter string, replacements []string) error {
		q := strings.NewReplacer(replacements...).Replace(query)
		return capacityRows(ctx, tx, `(SELECT * FROM `+from+` WHERE `+filter+`) batch`, q, []any{raw}, func(row pgx.Rows) error {
			var id string
			var v float64
			if err := row.Scan(&id, &v); err != nil {
				return err
			}
			s.limitUse[id] += v
			return nil
		})
	}
	if err = add(capacityNativeHeldSQL, `true`, []string{"$1", "batch.id", "$2", "''", "$3", "batch.start", "$4", "batch.now", "$5", "batch.unit", "$6", "batch.estimate"}); err != nil {
		return err
	}
	if err = add(capacityPercentSQL, `unit='percent'`, []string{"$1", "batch.id", "$2", "batch.start", "$3", "batch.now"}); err != nil {
		return err
	}
	if err = add(capacityPercentHeldSQL, `unit='percent'`, []string{"$1", "batch.id", "$2", "''", "$3", "batch.start", "$4", "batch.now"}); err != nil {
		return err
	}
	if err = add(capacityRunsSQL, `unit='runs'`, []string{"$1", "batch.id", "$2", "''", "$3", "batch.start", "$4", "batch.now"}); err != nil {
		return err
	}
	if err = add(capacityUnitsSQL, `unit NOT IN ('runs','percent')`, []string{"$1", "batch.id", "$2", "batch.unit", "$3", "batch.start", "$4", "batch.now", "$5", "''", "$6", "batch.estimate"}); err != nil {
		return err
	}
	return nil
}
