// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type readingsWrite struct {
	Readings []capacity.Reading `json:"readings"`
}

func (m *Module) ingestReadings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := requireAgent(p); err != nil {
		writeErr(w, err)
		return
	}
	var in readingsWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if err := requireScope(r.Context(), tx, r, p, "account.probe"); err != nil {
			return err
		}
		return ingestReadings(r.Context(), tx, p, r.PathValue("accountId"), in.Readings)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func ingestReadings(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, readings []capacity.Reading) error {
	if !uuidRE.MatchString(id) {
		return fail(404, "account not found")
	}
	if len(readings) < 1 || len(readings) > 32 {
		return fail(400, "one to 32 readings required")
	}
	if err := agentpairing.AccountFence(ctx, tx, id, true); err != nil {
		return err
	}
	a, err := lockAccount(ctx, tx, id)
	if err != nil {
		return err
	}
	if a.RegisteredBy != p.ID {
		return fail(403, "only the registering agent can report capacity")
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return err
	}
	for _, v := range readings {
		if err := v.Validate(now); err != nil {
			return fail(400, err.Error())
		}
		if looksLikeCredential(v.Plan) || looksLikeCredential(v.Bucket) {
			return fail(400, "invalid display metadata")
		}
		var run any
		if v.RunID != "" {
			if !uuidRE.MatchString(v.RunID) {
				return fail(400, "invalid run id")
			}
			var own bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE id=$1 AND account_id=$2)`, v.RunID, id).Scan(&own); err != nil {
				return err
			}
			if !own {
				return fail(403, "reading run does not use this account")
			}
			run = v.RunID
		}
		tag, err := tx.Exec(ctx, `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source,plan,ordinary_usage_allowed,run_id,phase) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT DO NOTHING`, p.TenantID, id, v.WindowKind, v.Bucket, v.WindowMinutes, v.UsedPercent, v.ResetsAt, v.ReadAt, v.Source, v.Plan, v.OrdinaryUsageAllowed, run, v.Phase)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		// Estimates never replace a measured observation. Within a timestamp the
		// harness wins over the daemon; delayed/out-of-order samples stay history.
		var newer bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_capacity_readings WHERE account_id=$1 AND window_kind=$2 AND bucket=$3 AND source<>'estimate' AND (read_at>$4 OR (read_at=$4 AND source='harness' AND $5<>'harness')))`, id, v.WindowKind, v.Bucket, v.ReadAt, v.Source).Scan(&newer); err != nil {
			return err
		}
		if newer || v.Source == "estimate" {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE account_allowance_windows SET capacity_allowed=false WHERE account_id=$1 AND capacity_kind=$2 AND capacity_bucket=$3`, id, v.WindowKind, v.Bucket); err != nil {
			return err
		}
		// A missing authority bit is not recovery from a vendor denial,
		// including across reset. Only a later explicit vendor allowance clears it.
		allowed := true
		var authority *bool
		err = tx.QueryRow(ctx, `SELECT ordinary_usage_allowed FROM account_capacity_readings WHERE account_id=$1 AND source<>'estimate' AND ordinary_usage_allowed IS NOT NULL ORDER BY read_at DESC, CASE source WHEN 'harness' THEN 0 ELSE 1 END, ordinary_usage_allowed ASC LIMIT 1`, id).Scan(&authority)
		if err != nil && !isNoRows(err) {
			return err
		}
		if authority != nil {
			allowed = *authority
		}
		if !allowed {
			// Vendor denial also fences reservations against other windows.
			if _, err := tx.Exec(ctx, `UPDATE account_allowance_windows SET capacity_allowed=false WHERE account_id=$1 AND capacity_kind IS NOT NULL`, id); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model,burst_ratio,capacity_kind,capacity_bucket,capacity_read_at,capacity_allowed)
   VALUES($1,$2,$3,$4,'percent',100,$5,'unrestricted',0,$6,$7,$8,$9)
   ON CONFLICT(tenant_id,account_id,capacity_kind,capacity_bucket,ends_at) WHERE capacity_kind IS NOT NULL
   DO UPDATE SET starts_at=EXCLUDED.starts_at,used=EXCLUDED.used,capacity_read_at=EXCLUDED.capacity_read_at,capacity_allowed=EXCLUDED.capacity_allowed`, p.TenantID, id, v.StartsAt(), v.ResetsAt, int64(math.Ceil(v.UsedPercent)), v.WindowKind, v.Bucket, v.ReadAt, allowed)
		if err != nil {
			return err
		}
	}
	return nil
}

func readCapacity(ctx context.Context, tx pgx.Tx, id string, current bool) ([]capacity.Reading, error) {
	query := `SELECT window_kind,bucket,window_minutes,used_percent::float8,resets_at,read_at,source,plan,ordinary_usage_allowed,COALESCE(run_id::text,''),phase FROM account_capacity_readings WHERE account_id=$1 ORDER BY read_at DESC, CASE source WHEN 'harness' THEN 0 WHEN 'agentd' THEN 1 ELSE 2 END LIMIT 200`
	if current {
		query = `SELECT window_kind,bucket,window_minutes,used_percent::float8,resets_at,read_at,source,plan,ordinary_usage_allowed,COALESCE(run_id::text,''),phase FROM (SELECT DISTINCT ON(window_kind,bucket) * FROM account_capacity_readings WHERE account_id=$1 ORDER BY window_kind,bucket,read_at DESC, CASE source WHEN 'harness' THEN 0 WHEN 'agentd' THEN 1 ELSE 2 END) r ORDER BY resets_at,window_kind,bucket`
	}
	rows, err := tx.Query(ctx, query, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []capacity.Reading{}
	for rows.Next() {
		var v capacity.Reading
		if err := rows.Scan(&v.WindowKind, &v.Bucket, &v.WindowMinutes, &v.UsedPercent, &v.ResetsAt, &v.ReadAt, &v.Source, &v.Plan, &v.OrdinaryUsageAllowed, &v.RunID, &v.Phase); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (m *Module) capacityHistory(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.read"); err != nil {
		writeErr(w, err)
		return
	}
	var out []capacity.Reading
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		id := r.PathValue("accountId")
		if !uuidRE.MatchString(id) {
			return fail(404, "account not found")
		}
		if _, err := getAccount(r.Context(), tx, id); err != nil {
			return fail(404, "account not found")
		}
		var err error
		out, err = readCapacity(r.Context(), tx, id, false)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

type capacityWindow struct {
	Reading         capacity.Reading `json:"reading"`
	StartsAt        time.Time        `json:"starts_at"`
	Allowance       int              `json:"allowance"`
	Remaining       float64          `json:"remaining_percent"`
	Freshness       string           `json:"freshness"`
	Pacing          capacity.Pacing  `json:"pacing"`
	UsageTodayKnown bool             `json:"usage_today_known"`
}
type accountCapacity struct {
	AccountID          string            `json:"account_id"`
	OngoingUseApproved bool              `json:"ongoing_use_approved"`
	Schedule           capacity.Schedule `json:"schedule"`
	Windows            []capacityWindow  `json:"windows"`
}

func (m *Module) capacityList(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.read"); err != nil {
		writeErr(w, err)
		return
	}
	out := []accountCapacity{}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		accounts, err := listAccounts(r.Context(), tx)
		if err != nil {
			return err
		}
		now, err := dbNow(r.Context(), tx)
		if err != nil {
			return err
		}
		for _, a := range accounts {
			s, err := effectiveSchedule(r.Context(), tx, p.ID, a)
			if err != nil {
				return err
			}
			item := accountCapacity{AccountID: a.ID, Schedule: s, Windows: []capacityWindow{}}
			if err := tx.QueryRow(r.Context(), `SELECT NOT EXISTS(SELECT 1 FROM agent_pairing_enrollments WHERE account_id=$1 AND (ongoing_approved_at IS NULL OR state<>'connected'))`, a.ID).Scan(&item.OngoingUseApproved); err != nil {
				return err
			}
			readings, err := readCapacity(r.Context(), tx, a.ID, true)
			if err != nil {
				return err
			}
			for _, v := range readings {
				left := 100 - v.UsedPercent
				periodStart, _ := s.Period(now)
				known := !v.StartsAt().Before(periodStart)
				usedToday := v.UsedPercent
				if !known && !v.ReadAt.Before(periodStart) {
					var baseline float64
					err := tx.QueryRow(r.Context(), `SELECT used_percent::float8 FROM account_capacity_readings WHERE account_id=$1 AND window_kind=$2 AND bucket=$3 AND resets_at=$4 AND read_at=$5 AND source<>'estimate' ORDER BY CASE source WHEN 'harness' THEN 0 ELSE 1 END LIMIT 1`, a.ID, v.WindowKind, v.Bucket, v.ResetsAt, periodStart).Scan(&baseline)
					if err != nil && !isNoRows(err) {
						return err
					}
					known = err == nil && baseline <= v.UsedPercent
					usedToday = v.UsedPercent - baseline
				}
				input := capacity.PlanInput{Now: now, Reset: v.ResetsAt, WindowStart: v.StartsAt(), Remaining: left, UsedToday: usedToday}
				if !known {
					input.WindowStart = now
					input.UsedToday = 0
				}
				pace, err := capacity.Plan(input, s)
				if err != nil {
					return err
				}
				item.Windows = append(item.Windows, capacityWindow{v, v.StartsAt(), 100, left, v.Freshness(now), pace, known})
			}
			out = append(out, item)
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

type scheduleOverride struct {
	Scope     string             `json:"scope"`
	Pool      string             `json:"pool,omitempty"`
	AccountID string             `json:"account_id,omitempty"`
	Schedule  *capacity.Schedule `json:"schedule"`
}

func (m *Module) capacitySchedule(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	permission := "account.read"
	if r.Method == "PUT" {
		permission = "account.manage"
	}
	if err := m.requirePermission(r, p, permission); err != nil {
		writeErr(w, err)
		return
	}
	var in scheduleOverride
	if r.Method == "PUT" {
		if err := decodeJSON(w, r, &in); err != nil {
			writeErr(w, err)
			return
		}
	}
	out := []scheduleOverride{}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if r.Method == "GET" {
			rows, err := tx.Query(r.Context(), `SELECT scope,scope_key,schedule FROM account_capacity_schedules WHERE principal_id=$1 ORDER BY scope,scope_key`, p.ID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var v scheduleOverride
				var key string
				var raw []byte
				if err := rows.Scan(&v.Scope, &key, &raw); err != nil {
					return err
				}
				if v.Scope == "pool" {
					v.Pool = key
				}
				if v.Scope == "account" {
					v.AccountID = key
				}
				if err := json.Unmarshal(raw, &v.Schedule); err != nil {
					return err
				}
				out = append(out, v)
			}
			return rows.Err()
		}
		key := ""
		var account any
		switch in.Scope {
		case "user":
			if in.Pool != "" || in.AccountID != "" {
				return fail(400, "invalid schedule scope")
			}
		case "pool":
			if !validHarness(in.Pool) || in.AccountID != "" {
				return fail(400, "invalid pool")
			}
			key = in.Pool
		case "account":
			if !uuidRE.MatchString(in.AccountID) || in.Pool != "" {
				return fail(400, "invalid account")
			}
			key = strings.ToLower(in.AccountID)
			if _, err := getAccount(r.Context(), tx, key); err != nil {
				return fail(404, "account not found")
			}
			account = key
		default:
			return fail(400, "invalid schedule scope")
		}
		if in.Schedule == nil {
			_, err := tx.Exec(r.Context(), `DELETE FROM account_capacity_schedules WHERE principal_id=$1 AND scope=$2 AND scope_key=$3`, p.ID, in.Scope, key)
			return err
		}
		if err := in.Schedule.Validate(); err != nil {
			return fail(400, err.Error())
		}
		raw, _ := json.Marshal(in.Schedule)
		_, err := tx.Exec(r.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(tenant_id,principal_id,scope,scope_key) DO UPDATE SET schedule=EXCLUDED.schedule`, p.TenantID, p.ID, in.Scope, key, account, raw)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if r.Method == "PUT" {
		w.WriteHeader(204)
	} else {
		httpapi.WriteJSON(w, 200, out)
	}
}
func effectiveSchedule(ctx context.Context, tx pgx.Tx, person string, a Account) (capacity.Schedule, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT schedule FROM account_capacity_schedules WHERE principal_id=$1 AND ((scope='user' AND scope_key='') OR (scope='pool' AND scope_key=$2) OR (scope='account' AND scope_key=$3)) ORDER BY CASE scope WHEN 'account' THEN 0 WHEN 'pool' THEN 1 ELSE 2 END LIMIT 1`, person, a.Harness, a.ID).Scan(&raw)
	if isNoRows(err) {
		return capacity.DefaultSchedule(), nil
	}
	if err != nil {
		return capacity.Schedule{}, err
	}
	var s capacity.Schedule
	err = json.Unmarshal(raw, &s)
	return s, err
}

// Approval belongs to a person, never to a quota reporter. This preserves the
// existing one-shot verification boundary while removing manual unit setup.
func (m *Module) approveCapacity(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.manage"); err != nil {
		writeErr(w, err)
		return
	}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		id := r.PathValue("accountId")
		if !uuidRE.MatchString(id) {
			return fail(404, "account not found")
		}
		if err := agentpairing.AccountFence(r.Context(), tx, id, false); err != nil {
			return err
		}
		if _, err := lockAccount(r.Context(), tx, id); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `UPDATE agent_pairing_enrollments SET ongoing_approved_at=clock_timestamp() WHERE account_id=$1 AND state='connected' AND ongoing_approved_at IS NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		return writeEvent(r.Context(), tx, p, "account.capacity_approved", nil, map[string]string{"account_id": id})
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *scheduleOverride) UnmarshalJSON(raw []byte) error {
	type wire scheduleOverride
	var v wire
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	if _, ok := obj["schedule"]; !ok {
		return errors.New("schedule is required; use null to remove an override")
	}
	*s = scheduleOverride(v)
	return nil
}
