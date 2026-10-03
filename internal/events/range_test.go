// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestEventBriefingRange(t *testing.T) {
	d, a, b := fixture(t)
	start := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, a.TenantID, func(tx pgx.Tx) error {
		for i, at := range []time.Time{start.Add(-time.Second), start, end.Add(-time.Second), end} {
			_, err := Append(t.Context(), tx, a, Change{Type: "test.changed", After: map[string]int{"row": i}, At: &at})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	appendEvents(t, d, b, 1)
	m := New(d.App).(*module)
	got, err := m.read(t.Context(), a, "", 0, 1, eventRange{from: &start, to: &end})
	if err != nil || len(got.Items) != 1 || got.Items[0].ID != 2 || got.NextAfter == nil {
		t.Fatalf("first page %+v %v", got, err)
	}
	next, err := m.read(t.Context(), a, "", *got.NextAfter, 1, eventRange{from: &start, to: &end})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != 3 || next.NextAfter != nil {
		t.Fatalf("next page %+v %v", next, err)
	}
	// Type filters never change cursor semantics or return unrelated snapshots.
	filtered, err := m.read(t.Context(), a, "", 0, 50, eventRange{from: &start, to: &end, types: []string{"node.updated"}})
	if err != nil || len(filtered.Items) != 0 {
		t.Fatalf("type filter %+v %v", filtered, err)
	}
	for _, query := range []string{"from=bad&to=bad", "from=2026-10-01T08:00:00Z", "from=2026-10-01T08:00:00Z&to=2026-09-30T08:00:00Z", "type=", "type=a,b,c,d,e,f,g,h,i"} {
		r := httptest.NewRequest(http.MethodGet, "/api/events?"+query, nil)
		r = r.WithContext(tenant.WithPrincipal(r.Context(), a))
		w := httptest.NewRecorder()
		m.list(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid query %s: %d", query, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/events?from=2026-09-30T08:00:00Z&to=2026-10-01T08:00:00Z&type=test.changed", nil)
	r = r.WithContext(tenant.WithPrincipal(r.Context(), a))
	w := httptest.NewRecorder()
	m.list(w, r)
	var page page
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Items) != 2 {
		t.Fatalf("HTTP range %d %s %v", w.Code, w.Body.String(), err)
	}
}

// These assertions exercise the HTTP contract, including timestamp/ID order
// differing from append order. The legacy ID stream remains unchanged.
func TestBriefingSnapshotAndTimeCursorRegression(t *testing.T) {
	d, p, _ := fixture(t)
	now := time.Now().UTC()
	ats := []time.Time{now.Add(-time.Hour), now.Add(-3 * time.Hour), now.Add(-time.Hour)}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		for _, at := range ats {
			if _, err := Append(t.Context(), tx, p, Change{Type: "test.changed", After: map[string]bool{"test": true}, At: &at}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m := New(d.App).(*module)
	call := func(query string) map[string]json.RawMessage {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/events?"+query, nil)
		req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
		w := httptest.NewRecorder()
		m.list(w, req)
		if w.Code != 200 {
			t.Fatalf("snapshot HTTP %d: %s", w.Code, w.Body.String())
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	first := call("briefing=true&type=test.changed&limit=1")
	var window struct {
		From, To      time.Time
		First, Capped bool
	}
	if err := json.Unmarshal(first["window"], &window); err != nil {
		t.Fatalf("no database window: %v", err)
	}
	if !window.First || window.To.Before(now) || window.To.After(time.Now().Add(time.Second)) || window.To.Sub(window.From) != 24*time.Hour {
		t.Fatalf("invalid window %+v", window)
	}
	var rows []Event
	_ = json.Unmarshal(first["items"], &rows)
	if len(rows) != 1 || rows[0].ID != 2 {
		t.Fatalf("not timestamp ordered: %+v", rows)
	}
	ids := []int64{rows[0].ID}
	page := first
	for i := 0; i < 2; i++ {
		var cursor string
		_ = json.Unmarshal(page["next_cursor"], &cursor)
		if cursor == "" {
			t.Fatal("missing time cursor")
		}
		page = call("from=" + url.QueryEscape(window.From.Format(time.RFC3339Nano)) + "&to=" + url.QueryEscape(window.To.Format(time.RFC3339Nano)) + "&cursor=" + url.QueryEscape(cursor) + "&type=test.changed&limit=1")
		_ = json.Unmarshal(page["items"], &rows)
		if len(rows) != 1 {
			t.Fatal("lost page")
		}
		ids = append(ids, rows[0].ID)
	}
	if ids[0] != 2 || ids[1] != 1 || ids[2] != 3 {
		t.Fatalf("skipped or repeated timestamp tie: %v", ids)
	}
	// Saved cutoffs come from PostgreSQL, which stores microsecond timestamps.
	future := now.Add(2 * time.Hour).Truncate(time.Microsecond)
	got := call("briefing=true&since=" + url.QueryEscape(future.Format(time.RFC3339Nano)))
	if err := json.Unmarshal(got["window"], &window); err != nil {
		t.Fatal(err)
	}
	if window.First || !window.From.Equal(future) || !window.To.Equal(future) {
		t.Fatalf("backward clock replay: %+v", window)
	}
}

func TestBriefingClockWindowCapUsesElapsedDaysRegression(t *testing.T) {
	d, p, _ := fixture(t)
	old := time.Now().Add(-400 * 24 * time.Hour)
	m := New(d.App).(*module)
	got, err := m.read(t.Context(), p, "", 0, 50, eventRange{briefing: true, since: &old})
	if err != nil {
		t.Fatal(err)
	}
	if got.Window == nil || got.Window.First || !got.Window.Capped || got.Window.To.Sub(got.Window.From) != 366*24*time.Hour {
		t.Fatalf("wrong elapsed-day cap: %+v", got.Window)
	}
}

func TestBriefingStatementCutoffDoesNotConsumeLaterSnapshotCommits(t *testing.T) {
	d, p, _ := fixture(t)
	visible := appendEvents(t, d, p, 1)[0]
	ctx := t.Context()
	blocker, err := d.Admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	const lock = 45402
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_lock($1)`, lock); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = blocker.Exec(ctx, `SELECT pg_advisory_unlock($1)`, lock) }()

	// Delay only the clock CTE's evaluation, after its statement snapshot has
	// been acquired. The rest is the exact first-page production statement.
	query := strings.Replace(briefingWindowSQL, " AS at)", " AS at FROM pg_advisory_xact_lock($5))", 1)
	if query == briefingWindowSQL {
		t.Fatal("clock barrier was not installed")
	}
	var window briefingWindow
	var body []byte
	pid := make(chan int32, 1)
	done := make(chan error, 1)
	go func() {
		done <- db.InTenant(tenant.WithPrincipal(ctx, p), d.App, p.TenantID, func(tx pgx.Tx) error {
			var id int32
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&id); err != nil {
				return err
			}
			pid <- id
			return tx.QueryRow(ctx, query, p.TenantID, nil, []string{"test.changed"}, 51, lock).Scan(&window.From, &window.To, &window.First, &window.Capped, &body)
		})
	}()
	var reader int32
	select {
	case reader = <-pid:
	case err := <-done:
		t.Fatalf("reader failed before barrier: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not start")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked bool
		if err := d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND NOT granted)`, reader).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reader did not acquire its snapshot before the clock barrier")
		}
		time.Sleep(5 * time.Millisecond)
	}
	late := appendEvents(t, d, p, 1)[0]
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_unlock($1)`, lock); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var items []Event
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != visible.ID || !visible.At.Before(window.To) {
		t.Fatalf("snapshot-visible row missing from window: %+v %+v", window, items)
	}
	if late.At.Before(window.To) {
		t.Fatal("cutoff consumed the row committed after the statement snapshot")
	}
	// Saving this cutoff must leave the concurrent commit for the next visit.
	next, err := New(d.App).(*module).read(ctx, p, "", 0, 50, eventRange{briefing: true, since: &window.To, types: []string{"test.changed"}})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != late.ID {
		t.Fatalf("next visit skipped concurrent commit: %+v %v", next, err)
	}
}
