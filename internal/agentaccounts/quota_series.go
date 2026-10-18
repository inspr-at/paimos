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
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Persist only normalized measurements. Vendor replies, identities, paths,
// plans, credentials and diagnostic text never enter these snapshots.
type quotaSample struct {
	WindowKind  string    `json:"window_kind"`
	Bucket      string    `json:"bucket"`
	UsedPercent float64   `json:"used_percent"`
	ReadAt      time.Time `json:"read_at"`
	ResetsAt    time.Time `json:"resets_at"`
	Source      string    `json:"source"`
}

type quotaSnapshot struct {
	BindingRevision int64         `json:"binding_revision"`
	Samples         []quotaSample `json:"samples"`
}

type quotaReading struct {
	quotaSample
	RemainingPercent  float64 `json:"remaining_percent"`
	ReadingAgeSeconds int64   `json:"reading_age_seconds"`
	Freshness         string  `json:"freshness"`
}

type quotaHour struct {
	HourAt   time.Time      `json:"hour_at"`
	Readings []quotaReading `json:"readings"`
}

type quotaSeries struct {
	AccountID      string         `json:"account_id"`
	LatestReadings []quotaReading `json:"latest_readings"`
	HourlyReadings []quotaHour    `json:"hourly_readings"`
	NextCursor     string         `json:"next_cursor"`
}

func quotaMinutes(kind string) int {
	switch kind {
	case "5h":
		return 300
	case "weekly":
		return 10080
	}
	return 0
}

func (v quotaSample) reading() capacity.Reading {
	return capacity.Reading{WindowKind: v.WindowKind, Bucket: v.Bucket, WindowMinutes: quotaMinutes(v.WindowKind), UsedPercent: v.UsedPercent, ReadAt: v.ReadAt, ResetsAt: v.ResetsAt, Source: v.Source}
}

func quotaReadings(samples []quotaSample, kind string, now time.Time) []quotaReading {
	out := []quotaReading{}
	for _, v := range samples {
		if kind == "" || v.WindowKind == kind {
			out = append(out, quotaReading{v, 100 - v.UsedPercent, max(0, int64(now.Sub(v.ReadAt)/time.Second)), v.reading().Freshness(now)})
		}
	}
	return out
}

func mergeQuotaSamples(old, incoming []quotaSample) ([]quotaSample, error) {
	byWindow := map[string]quotaSample{}
	for _, samples := range [][]quotaSample{old, incoming} {
		for _, v := range samples {
			key := v.WindowKind + ":" + v.Bucket
			previous, ok := byWindow[key]
			if !ok || v.ReadAt.After(previous.ReadAt) || v.ReadAt.Equal(previous.ReadAt) && v.Source == "harness" && previous.Source != "harness" {
				byWindow[key] = v
			}
		}
	}
	if len(byWindow) > 32 {
		return nil, fail(400, "too many quota windows")
	}
	out := make([]quotaSample, 0, len(byWindow))
	for _, v := range byWindow {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WindowKind+":"+out[i].Bucket < out[j].WindowKind+":"+out[j].Bucket })
	return out, nil
}

// The caller holds the account lock and its final authority/binding fence.
// Replays use the reading's original UTC hour, never delivery time. Updates
// merge sparse windows and prefer newer samples, then harness authority on ties.
func storeQuotaSamples(ctx context.Context, tx pgx.Tx, tenantID string, a Account, samples []quotaSample, now time.Time) error {
	if a.Harness != "claude" && a.Harness != "codex" || len(samples) == 0 {
		return nil
	}
	if len(samples) > 32 {
		return fail(400, "too many quota samples")
	}
	byHour := map[time.Time][]quotaSample{}
	for _, v := range samples {
		if quotaMinutes(v.WindowKind) == 0 || v.Source != "harness" && v.Source != "agentd" || v.reading().Validate(now) != nil || looksLikeCredential(v.Bucket) {
			return fail(400, "invalid quota sample")
		}
		hour := v.ReadAt.UTC().Truncate(time.Hour)
		byHour[hour] = append(byHour[hour], v)
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT quota_latest FROM agent_accounts WHERE id=$1`, a.ID).Scan(&raw); err != nil {
		return err
	}
	var latest quotaSnapshot
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &latest); err != nil {
			return err
		}
	}
	if latest.BindingRevision != a.LinkRevision {
		latest = quotaSnapshot{BindingRevision: a.LinkRevision}
	}
	var err error
	latest.Samples, err = mergeQuotaSamples(latest.Samples, samples)
	if err != nil {
		return err
	}
	hours := make([]time.Time, 0, len(byHour))
	for hour := range byHour {
		hours = append(hours, hour)
	}
	sort.Slice(hours, func(i, j int) bool { return hours[i].Before(hours[j]) })
	for _, hour := range hours {
		var revision int64
		var previous []quotaSample
		err := tx.QueryRow(ctx, `SELECT binding_revision,samples FROM account_quota_hourly WHERE account_id=$1 AND hour_at=$2`, a.ID, hour).Scan(&revision, &raw)
		if err != nil && !isNoRows(err) {
			return err
		}
		if err == nil && revision == a.LinkRevision {
			if err := json.Unmarshal(raw, &previous); err != nil {
				return err
			}
		}
		merged, err := mergeQuotaSamples(previous, byHour[hour])
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO account_quota_hourly(tenant_id,account_id,hour_at,binding_revision,samples) VALUES($1,$2,$3,$4,$5)
		 ON CONFLICT(tenant_id,account_id,hour_at) DO UPDATE SET binding_revision=EXCLUDED.binding_revision,samples=EXCLUDED.samples
		 WHERE account_quota_hourly.binding_revision IS DISTINCT FROM EXCLUDED.binding_revision OR account_quota_hourly.samples IS DISTINCT FROM EXCLUDED.samples`, tenantID, a.ID, hour, a.LinkRevision, merged); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE agent_accounts SET quota_latest=$2 WHERE id=$1 AND quota_latest IS DISTINCT FROM $2::jsonb`, a.ID, latest)
	return err
}

// UTC hour boundaries make the meaning of an hourly rollup explicit. An
// arbitrary sub-hour range cannot reconstruct samples already coalesced away.
func quotaHourParameter(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || !t.Equal(t.UTC().Truncate(time.Hour)) {
		return time.Time{}, fail(400, "quota range requires UTC hour boundaries")
	}
	return t.UTC(), nil
}

func (m *Module) quotaSeries(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	id := r.PathValue("accountId")
	if !uuidRE.MatchString(id) {
		writeErr(w, fail(404, "account not found"))
		return
	}
	q := r.URL.Query()
	from, err := quotaHourParameter(q.Get("from"))
	if err != nil {
		writeErr(w, err)
		return
	}
	to, err := quotaHourParameter(q.Get("to"))
	if err != nil || !to.After(from) || to.Sub(from) > 31*24*time.Hour {
		writeErr(w, fail(400, "quota range must be between one hour and 31 days"))
		return
	}
	kind := q.Get("window_kind")
	if kind != "" && quotaMinutes(kind) == 0 {
		writeErr(w, fail(400, "invalid quota window"))
		return
	}
	limit := 168
	if value, ok := q["limit"]; ok {
		limit, err = strconv.Atoi(value[0])
		if err != nil || limit < 1 || limit > 168 {
			writeErr(w, fail(400, "invalid quota page limit"))
			return
		}
	}
	cursor := from.Add(-time.Hour)
	if value := q.Get("cursor"); value != "" {
		cursor, err = quotaHourParameter(value)
		if err != nil || cursor.Before(from) || !cursor.Before(to) {
			writeErr(w, fail(400, "invalid quota cursor"))
			return
		}
	}
	out := quotaSeries{AccountID: id, LatestReadings: []quotaReading{}, HourlyReadings: []quotaHour{}}
	err = m.read(ctx, p.TenantID, func(tx pgx.Tx) error {
		if p.Kind == tenant.Agent {
			var err error
			p.Scopes, err = readKeyScopes(ctx, tx, r, p, false)
			if err != nil {
				return err
			}
			if !hasScope(p.Scopes, "account.probe") || authz.RequireTx(ctx, tx, p, "account.probe", authz.Scope{}) != nil {
				return fail(403, "account probe permission required")
			}
		} else if p.Kind != tenant.Person || authz.RequireTx(ctx, tx, p, "account.read", authz.Scope{}) != nil {
			return fail(403, "account read permission required")
		}
		a, err := loadAccount(ctx, tx, id)
		if err != nil {
			return accountNotFound(err)
		}
		if p.Kind == tenant.Agent && a.RegisteredBy != p.ID {
			return fail(403, "only the registering agent can read quota")
		}
		policy, err := accountprivacy.Load(ctx, tx, p, []string{id})
		if err != nil || !policy[id] {
			return err
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT quota_latest FROM agent_accounts WHERE id=$1`, id).Scan(&raw); err != nil {
			return err
		}
		if len(raw) > 0 {
			var latest quotaSnapshot
			if err := json.Unmarshal(raw, &latest); err != nil {
				return err
			}
			if latest.BindingRevision == a.LinkRevision {
				out.LatestReadings = quotaReadings(latest.Samples, kind, now)
			}
		}
		rows, err := tx.Query(ctx, `SELECT hour_at,samples FROM account_quota_hourly WHERE account_id=$1 AND binding_revision=$2 AND hour_at>=$3 AND hour_at<$4 AND hour_at>$5 ORDER BY hour_at LIMIT $6`, id, a.LinkRevision, from, to, cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var hour quotaHour
			if err := rows.Scan(&hour.HourAt, &raw); err != nil {
				return err
			}
			if len(out.HourlyReadings) == limit {
				out.NextCursor = out.HourlyReadings[limit-1].HourAt.UTC().Format(time.RFC3339)
				break
			}
			var samples []quotaSample
			if err := json.Unmarshal(raw, &samples); err != nil {
				return err
			}
			hour.Readings = quotaReadings(samples, kind, now)
			out.HourlyReadings = append(out.HourlyReadings, hour)
		}
		return rows.Err()
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func quotaSamplesFromFacts(facts []ReadinessFactWrite, local string) []quotaSample {
	var out []quotaSample
	for _, f := range facts {
		kind, bucket, _ := strings.Cut(f.WindowKey, ":")
		if f.ResourceID == local && quotaMinutes(kind) != 0 && f.UsedPercent != nil && f.ReadingAt != nil && f.ResetsAt != nil && (f.Source == "agentd" || f.Source == "harness") {
			out = append(out, quotaSample{kind, bucket, *f.UsedPercent, *f.ReadingAt, *f.ResetsAt, f.Source})
		}
	}
	return out
}
