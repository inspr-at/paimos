// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func viewer(f *fixture) tenant.Principal {
	p := tenant.Principal{TenantID: f.p.TenantID, Kind: tenant.Person}
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Reader') RETURNING id::text`, f.p.TenantID).Scan(&p.ID)
	})
	dbtest.BindRole(f.t, f.d, p.TenantID, p.ID, "viewer")
	return p
}
func TestUIReadOnlyDraftHistoryAndRetirement(t *testing.T) {
	f := setup(t)
	in := f.input()
	in.Template.Name = "Weekly tool sweep"
	var before, after int
	f.tx(func(tx pgx.Tx) error { return tx.QueryRow(t.Context(), `SELECT count(*) FROM events`).Scan(&before) })
	var preview struct {
		Times []time.Time `json:"times"`
	}
	if err := json.Unmarshal(f.call(f.p, "POST", "/api/recurrences/preview", in, 200), &preview); err != nil || len(preview.Times) != 4 {
		t.Fatalf("draft %+v %v", preview, err)
	}
	f.tx(func(tx pgx.Tx) error { return tx.QueryRow(t.Context(), `SELECT count(*) FROM events`).Scan(&after) })
	if after != before {
		t.Fatal("draft preview wrote audit data")
	}
	r := f.create(in)
	first := f.manual(r.ID, "first")
	f.manual(r.ID, "skipped")
	got := f.get(r.ID)
	if got.Template.Name != in.Template.Name || got.LastResult == nil || got.LastResult.Outcome != "skipped" || got.OpenPrevious == nil || got.OpenPrevious.NodeKey == "" || got.OpenPrevious.Number != 1 {
		t.Fatalf("overview %+v", got)
	}
	reader := viewer(f)
	for _, path := range []string{"/api/recurrences?project_id=" + f.project, "/api/recurrences/" + r.ID, "/api/recurrences/" + r.ID + "/preview", "/api/recurrences/" + r.ID + "/history", "/api/recurrences/" + r.ID + "/releases"} {
		f.call(reader, "GET", path, nil, 200)
	}
	f.call(reader, "POST", "/api/recurrences/preview", in, 403)
	f.call(reader, "DELETE", "/api/recurrences/"+r.ID, map[string]int{"expected_revision": 1}, 403)
	var history struct {
		Items []HistoryEntry `json:"items"`
	}
	if err := json.Unmarshal(f.call(reader, "GET", "/api/recurrences/"+r.ID+"/history?filter=created", nil, 200), &history); err != nil || len(history.Items) != 1 || history.Items[0].Node["key"] != got.OpenPrevious.NodeKey {
		t.Fatalf("history %+v %v", history, err)
	}
	f.call(reader, "GET", "/api/recurrences/"+r.ID+"/history?before=bad", nil, 400)
	f.call(reader, "GET", "/api/recurrences/"+r.ID+"/history?filter=unknown", nil, 400)
	f.call(f.p, "DELETE", "/api/recurrences/"+r.ID, map[string]int{"expected_revision": 2}, 409)
	f.call(f.p, "DELETE", "/api/recurrences/"+r.ID, map[string]int{"expected_revision": 1}, 204)
	f.call(f.p, "GET", "/api/recurrences/"+r.ID, nil, 404)
	f.call(f.p, "POST", "/api/recurrences/"+r.ID+"/run-now", map[string]string{"idempotency_key": "later"}, 404)
	var page struct {
		Items []Recurrence `json:"items"`
	}
	if err := json.Unmarshal(f.call(reader, "GET", "/api/recurrences", nil, 200), &page); err != nil || len(page.Items) != 0 {
		t.Fatalf("retired list %+v %v", page, err)
	}
	f.now = f.now.Add(30 * 24 * time.Hour)
	f.run()
	if len(f.receipts(r.ID)) != 2 {
		t.Fatal("retired definition ran")
	}
	f.tx(func(tx pgx.Tx) error {
		var alive bool
		if err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND deleted_at IS NULL AND fields->>'recurrence_id'=$2)`, first.NodeID, r.ID).Scan(&alive); err != nil {
			return err
		}
		if !alive {
			t.Fatal("retirement removed ticket provenance")
		}
		return nil
	})
}

func TestUIReleasePickerForceOverlapAndSchedulerDeduplication(t *testing.T) {
	f := setup(t)
	in := f.input()
	in.Trigger = Trigger{Kind: "event", Event: "release.published"}
	in.Template.Title = "Audit {{release_name}} {{release_version}} #{{occurrence}}"
	r := f.create(in)
	prior := f.manual(r.ID, "prior")
	f.now = f.now.Add(time.Hour)
	pub := Publication{ProjectKey: "REC", ProjectID: f.project, Name: "Sunlit Sonde", Version: "261002130000.0.0", PublishedAt: f.now}
	f.m.WithHistory([]Publication{pub})
	var page struct {
		Items []ReleaseChoice `json:"items"`
	}
	if err := json.Unmarshal(f.call(f.p, "GET", "/api/recurrences/"+r.ID+"/releases", nil, 200), &page); err != nil || len(page.Items) != 1 || page.Items[0].Name != pub.Name || page.Items[0].Receipt != nil {
		t.Fatalf("choices %+v %v", page, err)
	}
	path := "/api/recurrences/" + r.ID + "/run-now"
	f.call(f.p, "POST", path, map[string]any{"idempotency_key": "stale", "expected_revision": 2}, 409)
	f.call(f.p, "POST", path, map[string]any{"idempotency_key": "invalid", "release_key": "another-project/version:v1"}, 404)
	body := map[string]any{"idempotency_key": "release-manual", "expected_revision": 1, "release_key": page.Items[0].Key, "force_overlap": true}
	var occurrence Occurrence
	if err := json.Unmarshal(f.call(f.p, "POST", path, body, 200), &occurrence); err != nil || occurrence.Outcome != "created" || occurrence.Number != 2 || occurrence.NodeID == nil {
		t.Fatalf("forced %+v %v", occurrence, err)
	}
	body["idempotency_key"] = "same-release-different-retry"
	var retry Occurrence
	json.Unmarshal(f.call(f.p, "POST", path, body, 200), &retry)
	if retry.Key != occurrence.Key || retry.Number != 2 {
		t.Fatalf("release retry %+v", retry)
	}
	f.run()
	f.run()
	if len(f.receipts(r.ID)) != 2 {
		t.Fatal("manual release was duplicated by scheduler")
	}
	json.Unmarshal(f.call(f.p, "GET", "/api/recurrences/"+r.ID+"/releases", nil, 200), &page)
	if len(page.Items) != 1 || page.Items[0].Receipt == nil || page.Items[0].Receipt.Number != 2 {
		t.Fatalf("attempted choices %+v", page)
	}
	f.tx(func(tx pgx.Tx) error {
		var title string
		if err := tx.QueryRow(t.Context(), `SELECT title FROM nodes WHERE id=$1`, occurrence.NodeID).Scan(&title); err != nil {
			return err
		}
		if title != "Audit Sunlit Sonde 261002130000.0.0 #2" {
			t.Fatal(title)
		}
		if *prior.NodeID == *occurrence.NodeID {
			t.Fatal("forced run reused earlier ticket")
		}
		return nil
	})
}

func TestUIDelayedPublicationUsesDatabaseClock(t *testing.T) {
	for _, tc := range []struct{ start, due string }{{"now", "2026-10-24T23:30:00Z"}, {"hour", "2026-10-25T00:30:00Z"}, {"morning", "2026-10-25T05:00:00Z"}} {
		t.Run(tc.start, func(t *testing.T) {
			f := setup(t)
			f.now = timestamp(t, "2026-10-24T23:00:00Z")
			in := f.input()
			in.Trigger = Trigger{Kind: "event", Event: "release.published", EventStart: tc.start, EventTimezone: "Europe/Vienna"}
			r := f.create(in)
			publication := Publication{ProjectKey: "REC", Name: "Autumn Sonde", Version: "v2", PublishedAt: timestamp(t, "2026-10-24T23:30:00Z")}
			f.m.WithHistory([]Publication{publication})
			due := timestamp(t, tc.due)
			f.now = due.Add(-time.Second)
			f.run()
			if len(f.receipts(r.ID)) != 0 {
				t.Fatal("delayed event ran early")
			}
			f.now = due
			f.run()
			f.run()
			receipts := f.receipts(r.ID)
			if len(receipts) != 1 || !receipts[0].ScheduledAt.Equal(due) {
				t.Fatalf("due receipts %+v", receipts)
			}
		})
	}
}

func TestUIHistoryPaginationNoCrossRecurrenceRows(t *testing.T) {
	f := setup(t)
	in := f.input()
	in.OverlapPolicy = "create"
	r := f.create(in)
	other := f.create(in)
	for i := 0; i < 52; i++ {
		f.manual(r.ID, fmt.Sprint(i))
	}
	f.manual(other.ID, "other")
	var first, second struct {
		Items  []HistoryEntry `json:"items"`
		Cursor *int64         `json:"next_cursor"`
	}
	json.Unmarshal(f.call(f.p, "GET", "/api/recurrences/"+r.ID+"/history?filter=created", nil, 200), &first)
	if len(first.Items) != 50 || first.Cursor == nil {
		t.Fatalf("first page %+v", first)
	}
	json.Unmarshal(f.call(f.p, "GET", fmt.Sprintf("/api/recurrences/%s/history?filter=created&before=%d", r.ID, *first.Cursor), nil, 200), &second)
	if len(second.Items) != 2 || second.Cursor != nil {
		t.Fatalf("second page %+v", second)
	}
	if first.Items[49].ID <= second.Items[0].ID {
		t.Fatal("pages overlap")
	}
}
