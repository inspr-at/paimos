// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/jackc/pgx/v5"
)

// currentCapacityWindows is the set of windows Sprint is bounded by: measured,
// not retired and not yet reset. The Sprint end stored on save and the
// limiting_reset shown per account both use it, so the UI promises what the
// server will enforce.
const currentCapacityWindows = `w.capacity_read_at IS NOT NULL AND NOT w.capacity_retired AND w.ends_at>now()`

// scheduleEntry is one stored schedule of a person: the user default, a pool
// (harness) or an account override.
type scheduleEntry struct {
	Scope, Key string
	Schedule   capacity.Schedule
}

func loadScheduleEntries(ctx context.Context, tx pgx.Tx, person string) ([]scheduleEntry, error) {
	rows, err := tx.Query(ctx, `SELECT scope,scope_key,schedule FROM account_capacity_schedules WHERE principal_id=$1 ORDER BY scope,scope_key`, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []scheduleEntry{}
	for rows.Next() {
		var e scheduleEntry
		var raw []byte
		if err := rows.Scan(&e.Scope, &e.Key, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &e.Schedule); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func personDefaultSchedule(ctx context.Context, tx pgx.Tx, person string) (capacity.Schedule, error) {
	var zone string
	err := tx.QueryRow(ctx, `SELECT timezone FROM personal_profiles WHERE principal_id=$1`, person).Scan(&zone)
	if err != nil && !isNoRows(err) {
		return capacity.Schedule{}, err
	}
	return capacity.DefaultSchedule(zone), nil
}

// userSchedule is the person's own schedule: the saved user entry, else the default.
func userSchedule(entries []scheduleEntry, fallback capacity.Schedule) capacity.Schedule {
	for _, e := range entries {
		if e.Scope == "user" {
			return e.Schedule
		}
	}
	return fallback
}

// sameShape compares schedules apart from Sprint/Hold and the zone: a pool or
// account entry with the same shape as the person's schedule only carries an override.
func sameShape(a, b capacity.Schedule) bool {
	norm := func(s capacity.Schedule) string {
		s.Override, s.OverrideUntil, s.Timezone = "", nil, ""
		raw, _ := json.Marshal(s)
		return string(raw)
	}
	return norm(a) == norm(b)
}

func activeOverride(s capacity.Schedule, now time.Time) bool {
	if s.Override == "" {
		return false
	}
	return s.Override != "sprint" || s.OverrideUntil == nil || now.Before(*s.OverrideUntil)
}

// carryDraft is the one inheritance rule for a new person schedule, used by the
// preview and by saving with carry_overrides. An entry that only carries
// Sprint/Hold on top of the previous person schedule follows the new schedule
// and keeps its override (keep), or is removed when its override has ended
// (!keep). Any other entry is the person's deliberate pool or account schedule
// and stays as it is (carried false).
func carryDraft(e scheduleEntry, previous, next capacity.Schedule, now time.Time) (s capacity.Schedule, keep, carried bool) {
	if e.Scope == "user" || !sameShape(e.Schedule, previous) {
		return e.Schedule, true, false
	}
	if !activeOverride(e.Schedule, now) {
		return capacity.Schedule{}, false, true
	}
	s = next
	s.Override, s.OverrideUntil = e.Schedule.Override, e.Schedule.OverrideUntil
	return s, true, true
}

// scheduleWithDraft is the schedule an account would follow once next is saved
// as the person's schedule: account entry, then pool entry, then next itself.
func scheduleWithDraft(entries []scheduleEntry, a Account, previous, next capacity.Schedule, now time.Time) capacity.Schedule {
	for _, want := range []scheduleEntry{{Scope: "account", Key: a.ID}, {Scope: "pool", Key: a.Harness}} {
		for _, e := range entries {
			if e.Scope != want.Scope || e.Key != want.Key {
				continue
			}
			if s, keep, _ := carryDraft(e, previous, next, now); keep {
				return s
			}
		}
	}
	return next
}

// saveCarried writes next as the person's schedule and carries every override
// entry by carryDraft, inside the caller's transaction: all or nothing.
func saveCarried(ctx context.Context, tx pgx.Tx, tenantID, person string, next capacity.Schedule) error {
	entries, err := loadScheduleEntries(ctx, tx, person)
	if err != nil {
		return err
	}
	fallback, err := personDefaultSchedule(ctx, tx, person)
	if err != nil {
		return err
	}
	previous := userSchedule(entries, fallback)
	now, err := dbNow(ctx, tx)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(next)
	if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'user','',NULL,$3) ON CONFLICT(tenant_id,principal_id,scope,scope_key) DO UPDATE SET schedule=EXCLUDED.schedule`, tenantID, person, raw); err != nil {
		return err
	}
	for _, e := range entries {
		s, keep, carried := carryDraft(e, previous, next, now)
		if !carried {
			continue
		}
		if !keep {
			if _, err := tx.Exec(ctx, `DELETE FROM account_capacity_schedules WHERE principal_id=$1 AND scope=$2 AND scope_key=$3`, person, e.Scope, e.Key); err != nil {
				return err
			}
			continue
		}
		raw, _ := json.Marshal(s)
		if _, err := tx.Exec(ctx, `UPDATE account_capacity_schedules SET schedule=$4 WHERE principal_id=$1 AND scope=$2 AND scope_key=$3`, person, e.Scope, e.Key, raw); err != nil {
			return err
		}
	}
	return nil
}

// ---------- Preview bounds ----------

// previewGuard bounds the capacity preview: a per-person rate (per replica), a
// concurrency limit, a deadline and the aggregate integration work. The preview
// is read-only but CPU-bound, so every editor keystroke must stay cheap.
type previewGuard struct {
	mu      sync.Mutex
	hits    map[string][]time.Time
	limit   int
	window  time.Duration
	slots   chan struct{}
	budget  int
	timeout time.Duration
}

const (
	previewRate    = 20
	previewWindow  = 10 * time.Second
	previewSlots   = 4
	previewTimeout = 3 * time.Second
	// previewBudget is the most half-hour integration steps one preview may walk
	// across all accounts and windows (about 57 year-long windows or 2,900 weekly ones).
	previewBudget = 1_000_000
)

func newPreviewGuard(limit int, window time.Duration, slots, budget int, timeout time.Duration) *previewGuard {
	return &previewGuard{hits: map[string][]time.Time{}, limit: limit, window: window, slots: make(chan struct{}, slots), budget: budget, timeout: timeout}
}

var defaultPreviewGuard = newPreviewGuard(previewRate, previewWindow, previewSlots, previewBudget, previewTimeout)

func (g *previewGuard) allow(person string, now time.Time) (bool, time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.hits) > 10_000 {
		for key, list := range g.hits {
			if len(list) == 0 || now.Sub(list[len(list)-1]) >= g.window {
				delete(g.hits, key)
			}
		}
	}
	current := g.hits[person][:0]
	for _, at := range g.hits[person] {
		if now.Sub(at) < g.window {
			current = append(current, at)
		}
	}
	if len(current) >= g.limit {
		g.hits[person] = current
		return false, g.window - now.Sub(current[0])
	}
	g.hits[person] = append(current, now)
	return true, 0
}

func (g *previewGuard) acquire() bool {
	select {
	case g.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (g *previewGuard) release() { <-g.slots }

func retryAfter(w http.ResponseWriter, wait time.Duration) {
	seconds := int((wait + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
}

// previewSteps is the half-hour integration work Plan does for one window,
// with headroom for its several passes over [start, reset).
func previewSteps(start, reset time.Time) int {
	if !reset.After(start) {
		return 1
	}
	return 4*int(reset.Sub(start)/(30*time.Minute)) + 4
}
