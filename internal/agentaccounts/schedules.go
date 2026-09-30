// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

// sameShape compares schedules apart from Sprint/Hold/Away, Keep for you and the
// zone: a pool or account entry with the same shape as the person's schedule
// only carries an override or its own reserve.
func sameShape(a, b capacity.Schedule) bool {
	norm := func(s capacity.Schedule) string {
		s.Override, s.OverrideUntil, s.Timezone, s.Reserve, s.ReservePercent = "", nil, "", "", 0
		raw, _ := json.Marshal(s)
		return string(raw)
	}
	return norm(a) == norm(b)
}

func activeOverride(s capacity.Schedule, now time.Time) bool { return s.ActiveOverride(now) != "" }

// carryDraft is the one inheritance rule for a new person schedule, used by the
// preview and by saving with carry_overrides. An entry that only carries
// Sprint/Hold or its own Keep for you on top of the previous person schedule
// follows the new schedule and keeps those (keep), or is removed when it has
// nothing left to carry (!keep). Any other entry is the person's deliberate pool
// or account schedule and stays as it is (carried false).
func carryDraft(e scheduleEntry, previous, next capacity.Schedule, now time.Time) (s capacity.Schedule, keep, carried bool) {
	if e.Scope == "user" || !sameShape(e.Schedule, previous) {
		return e.Schedule, true, false
	}
	active := activeOverride(e.Schedule, now)
	if !active && e.Schedule.Reserve == "" {
		return capacity.Schedule{}, false, true
	}
	s = next
	s.Override, s.OverrideUntil = "", nil
	if active {
		s.Override, s.OverrideUntil = e.Schedule.Override, e.Schedule.OverrideUntil
	}
	s.Reserve, s.ReservePercent = e.Schedule.Reserve, e.Schedule.ReservePercent
	return s, true, true
}

// poolReserve is a draft Keep for you for one pool; an empty mode inherits.
type poolReserve struct {
	Pool           string  `json:"pool"`
	Reserve        string  `json:"reserve"`
	ReservePercent float64 `json:"reserve_percent,omitempty"`
}

// scheduleWithDraft is the schedule an account would follow once next is saved
// as the person's schedule (with carry_overrides) and pools' reserves as drafted.
func scheduleWithDraft(entries []scheduleEntry, a Account, previous, next capacity.Schedule, pools []poolReserve, now time.Time) capacity.Schedule {
	after := []scheduleEntry{}
	for _, e := range entries {
		if e.Scope == "user" {
			continue
		}
		if s, keep, _ := carryDraft(e, previous, next, now); keep {
			after = append(after, scheduleEntry{e.Scope, e.Key, s})
		}
	}
	for _, r := range pools {
		found := false
		for i := range after {
			if after[i].Scope == "pool" && after[i].Key == r.Pool {
				after[i].Schedule.Reserve, after[i].Schedule.ReservePercent = r.Reserve, r.ReservePercent
				found = true
			}
		}
		if !found && r.Reserve != "" {
			s := next
			s.Override, s.OverrideUntil, s.Reserve, s.ReservePercent = "", nil, r.Reserve, r.ReservePercent
			after = append(after, scheduleEntry{"pool", r.Pool, s})
		}
	}
	return resolveSchedule(append(after, scheduleEntry{Scope: "user", Schedule: next}), a, next, now)
}

// resolveSchedule is the schedule an account follows: the most specific entry
// (account, group, pool, then the person's own), with the most specific
// explicit Keep for you (Auto when none is set). Away on the person's
// schedule reaches every pool that has no override of its own in force; a
// pool's Hold or Sprint wins over it.
func resolveSchedule(entries []scheduleEntry, a Account, fallback capacity.Schedule, now time.Time) capacity.Schedule {
	chain := []capacity.Schedule{}
	var user *capacity.Schedule
	wants := []scheduleEntry{{Scope: "account", Key: a.ID}}
	if a.GroupID != "" {
		wants = append(wants, scheduleEntry{Scope: "group", Key: a.GroupID})
	}
	wants = append(wants, scheduleEntry{Scope: "pool", Key: a.Harness}, scheduleEntry{Scope: "user"})
	for _, want := range wants {
		for _, e := range entries {
			if e.Scope == want.Scope && e.Key == want.Key {
				chain = append(chain, e.Schedule)
				if e.Scope == "user" {
					user = &e.Schedule
				}
			}
		}
	}
	if user == nil {
		chain = append(chain, fallback)
	}
	s := chain[0]
	if user != nil && !activeOverride(s, now) && user.ActiveOverride(now) == "away" {
		s.Override, s.OverrideUntil = user.Override, user.OverrideUntil
	}
	s.Reserve, s.ReservePercent = "", 0
	for _, c := range chain {
		if c.Reserve != "" {
			s.Reserve, s.ReservePercent = c.Reserve, c.ReservePercent
			break
		}
	}
	return s
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

// readBodyBy reads at most 1 MiB of request body before deadline. The
// connection gets a read deadline where the server supports it, and the handler
// stops waiting at the deadline either way (408), so a stalled upload costs
// neither a preview slot nor the caller's time.
func readBodyBy(w http.ResponseWriter, r *http.Request, deadline time.Time) ([]byte, error) {
	_ = http.NewResponseController(w).SetReadDeadline(deadline)
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	type result struct {
		raw []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		raw, err := io.ReadAll(body)
		done <- result{raw, err}
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case got := <-done:
		if got.err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(got.err, &tooLarge) {
				return nil, fail(http.StatusRequestEntityTooLarge, "request body too large")
			}
			return nil, fail(http.StatusRequestTimeout, "the preview request did not arrive in time")
		}
		return got.raw, nil
	case <-timer.C:
		_ = r.Body.Close()
		return nil, fail(http.StatusRequestTimeout, "the preview request did not arrive in time")
	case <-r.Context().Done():
		return nil, fail(http.StatusRequestTimeout, "the preview request was cancelled")
	}
}

// decodeStrict is decodeJSON for a body already read: one JSON value, no unknown fields.
func decodeStrict(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fail(http.StatusBadRequest, "invalid JSON request body")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fail(http.StatusBadRequest, "request body must contain one JSON value")
	}
	return nil
}
