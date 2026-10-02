// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
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

func projectPrincipal(f *fixture, role string) tenant.Principal {
	p := tenant.Principal{TenantID: f.p.TenantID, Kind: tenant.Person}
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(f.t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Project reader') RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		_, err := tx.Exec(f.t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key=$4`, p.TenantID, p.ID, f.project, role)
		return err
	})
	return p
}

func TestUIProjectScopedRecurrenceHistoryAndRetirement(t *testing.T) {
	f := setup(t)
	r := f.create(f.input())
	f.manual(r.ID, "first")
	f.manual(r.ID, "skip")
	reader := projectPrincipal(f, "viewer")
	manager := projectPrincipal(f, "member")
	var history struct {
		Items []HistoryEntry `json:"items"`
	}
	if err := json.Unmarshal(f.call(reader, "GET", "/api/recurrences/"+r.ID+"/history", nil, 200), &history); err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, item := range history.Items {
		types[item.Type] = true
	}
	for _, typ := range []string{"recurrence.created", "recurrence.occurred", "recurrence.skipped"} {
		if !types[typ] {
			t.Errorf("project reader cannot see %s", typ)
		}
	}
	f.call(f.p, "DELETE", "/api/recurrences/"+r.ID, map[string]int{"expected_revision": 1}, 204)
	ctx := tenant.WithPrincipal(t.Context(), reader)
	if err := db.InTenant(ctx, f.d.App, reader.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='recurrence.deleted' AND after->>'id'=$1`, r.ID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Errorf("project reader sees %d delete events, want 1", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []Recurrence `json:"items"`
	}
	if err := json.Unmarshal(f.call(reader, "GET", "/api/recurrences?project_id="+f.project, nil, 200), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Errorf("retired project-scoped list contains %d definitions", len(page.Items))
	}
	f.call(reader, "GET", "/api/recurrences/"+r.ID, nil, 404)
	f.call(manager, "POST", "/api/recurrences/"+r.ID+"/resume", map[string]int{"expected_revision": 2}, 404)
	f.call(manager, "POST", "/api/recurrences/"+r.ID+"/run-now", map[string]string{"idempotency_key": "retired"}, 404)
}

func TestUIRetiredSchedulerClaimIgnoresReactivatedRow(t *testing.T) {
	f := setup(t)
	r := f.create(f.input())
	f.call(projectPrincipal(f, "member"), "DELETE", "/api/recurrences/"+r.ID, map[string]int{"expected_revision": 1}, 204)
	// Simulate a row resumed by a pre-fix project-scoped client. Its tombstone
	// must exclude it at claim time, before parsing or recording a failure.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE recurrences SET paused=false,trigger=trigger||'{"rrule":"FREQ=INVALID"}'::jsonb WHERE id=$1`, r.ID)
		return err
	})
	good := f.create(f.input())
	f.now = f.now.Add(7 * 24 * time.Hour)
	f.run()
	if len(f.receipts(r.ID)) != 0 || len(f.receipts(good.ID)) != 1 {
		t.Fatal("retired row ran or prevented a healthy row from running")
	}
}

func TestUIProjectRecurrenceEventsPreserveVisibilityBoundaries(t *testing.T) {
	f := setup(t)
	reader := projectPrincipal(f, "viewer")
	other := f.node("project", nil, "Invisible project")
	ids := []int64{}
	for _, change := range []events.Change{
		{NodeID: &f.project, Type: "recurrence.updated", After: map[string]string{"name": "Visible"}},
		{NodeID: &other, Type: "recurrence.updated", After: map[string]string{"name": "Other project"}},
		{NodeID: &f.project, Type: "recurrence.updated", After: map[string]string{"node_id": other}},
		{NodeID: &f.project, Type: "run.started", After: map[string]string{"name": "Workspace telemetry"}},
		{Type: "recurrence.updated", After: map[string]string{"name": "Workspace event"}},
	} {
		f.tx(func(tx pgx.Tx) error {
			event, err := events.Append(t.Context(), tx, f.p, change)
			if err == nil {
				ids = append(ids, event.ID)
			}
			return err
		})
	}
	ctx := tenant.WithPrincipal(t.Context(), reader)
	if err := db.InTenant(ctx, f.d.App, reader.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM events WHERE id=ANY($1::bigint[]) ORDER BY id`, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		seen := []int64{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			seen = append(seen, id)
		}
		if len(seen) != 1 || seen[0] != ids[0] {
			t.Errorf("project recurrence visibility %v, want only %d", seen, ids[0])
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUIDeferredEventClaimDoesNotStarveReadyRow(t *testing.T) {
	f := setup(t)
	in := f.input()
	in.Trigger = Trigger{Kind: "event", Event: "release.published", EventStart: "hour"}
	deferred := f.create(in)
	in.Trigger.EventStart = "now"
	ready := f.create(in)
	// Fix ordering before any receipts exist so the deferred row wins LIMIT 1.
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE recurrences SET id=$2 WHERE id=$1`, deferred.ID, "10000000-0000-4000-8000-000000000010"); err != nil {
			return err
		}
		deferred.ID = "10000000-0000-4000-8000-000000000010"
		if _, err := tx.Exec(t.Context(), `UPDATE recurrences SET id=$2 WHERE id=$1`, ready.ID, "10000000-0000-4000-8000-000000000020"); err != nil {
			return err
		}
		ready.ID = "10000000-0000-4000-8000-000000000020"
		return nil
	})
	f.now = f.now.Add(time.Minute)
	f.m.WithHistory([]Publication{{ProjectKey: "REC", Name: "Release", Version: "v2", PublishedAt: f.now}})
	f.run()
	if len(f.receipts(deferred.ID)) != 0 || f.get(deferred.ID).EventCursor != 0 || len(f.receipts(ready.ID)) != 1 {
		t.Fatal("deferred row advanced early or prevented the ready row from running")
	}
	f.now = f.now.Add(time.Hour)
	f.run()
	if len(f.receipts(deferred.ID)) != 1 || len(f.receipts(ready.ID)) != 1 {
		t.Fatal("due row did not run once or ready row was duplicated")
	}
}

func TestUIClosedWorkflowCategoriesMatchOverlapAndOpenPrevious(t *testing.T) {
	for _, tc := range []struct {
		name, kind, state, category string
		closed                      bool
	}{
		{"configured done", "ticket", "resolved", "done", true},
		{"configured cancelled", "ticket", "withdrawn", "cancelled", true},
		{"configured archived", "task", "shelved", "archived", true},
		{"normalized category", "task", " READY--TO SHIP ", " Done ", true},
		{"fixed done overridden", "ticket", "done", "doing", false},
		{"fixed archived overridden", "task", "archived", "open", false},
		{"doing", "ticket", "testing", "in_progress", false},
		{"unknown category falls back", "task", " DONE ", "legacy", true},
		{"fixed canceled", "ticket", " Canceled ", "", true},
		{"fixed accepted", "task", "accepted", "", true},
		{"fixed delivered", "ticket", "delivered", "", true},
		{"unknown state stays open", "task", "uncatalogued", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			in := f.input()
			in.Template.Type = tc.kind
			r := f.create(in)
			first := f.manual(r.ID, "first")
			f.tx(func(tx pgx.Tx) error {
				catalog, err := json.Marshal([]map[string]string{{"state": tc.state, "category": tc.category}})
				if err != nil {
					return err
				}
				// Give the other kind the opposite category to detect a join that
				// accidentally ignores the occurrence node's kind.
				other := "task"
				if tc.kind == "task" {
					other = "ticket"
				}
				opposite := "done"
				if tc.closed {
					opposite = "open"
				}
				otherCatalog, err := json.Marshal([]map[string]string{{"state": tc.state, "category": opposite}})
				if err != nil {
					return err
				}
				if _, err = tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema=jsonb_set(field_schema,'{states}',$2::jsonb) WHERE slug=$1`, other, otherCatalog); err != nil {
					return err
				}
				if _, err = tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema=jsonb_set(field_schema,'{states}',$2::jsonb) WHERE slug=$1`, tc.kind, catalog); err != nil {
					return err
				}
				_, err = tx.Exec(t.Context(), `UPDATE nodes SET state=$2 WHERE id=$1`, first.NodeID, tc.state)
				return err
			})
			got := f.get(r.ID)
			if (got.OpenPrevious == nil) != tc.closed {
				t.Errorf("open_previous=%+v, closed=%v", got.OpenPrevious, tc.closed)
			}
			second := f.manual(r.ID, "second")
			want := "skipped"
			if tc.closed {
				want = "created"
			}
			if second.Outcome != want {
				t.Errorf("overlap outcome %s, want %s", second.Outcome, want)
			}
		})
	}
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
	readContext := authz.BindPool(tenant.WithPrincipal(t.Context(), reader), f.d.App)
	for _, pattern := range []string{"GET /api/recurrences", "GET /api/recurrences/{recurrenceId}", "GET /api/recurrences/{recurrenceId}/preview", "GET /api/recurrences/{recurrenceId}/history", "GET /api/recurrences/{recurrenceId}/releases"} {
		if err := authz.RequirePattern(readContext, pattern, authz.Scope{}); err != nil {
			t.Fatalf("viewer blocked at production boundary %s: %v", pattern, err)
		}
	}
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

func TestUIDraftPreviewAdmitsProjectOnlyManager(t *testing.T) {
	f := setup(t)
	manager := tenant.Principal{TenantID: f.p.TenantID, Kind: tenant.Person}
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Project manager') RETURNING id::text`, manager.TenantID).Scan(&manager.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, manager.TenantID, manager.ID, f.project)
		return err
	})
	ctx := authz.BindPool(tenant.WithPrincipal(t.Context(), manager), f.d.App)
	const pattern = "POST /api/recurrences/preview"
	if err := authz.RequirePattern(ctx, pattern, authz.Scope{}); err == nil {
		t.Fatal("project-only grant reached workspace boundary")
	}
	scope, ok, err := authz.ResolveRouteScope(ctx, f.d.App, pattern, "/api/recurrences/preview")
	if err != nil || !ok || !scope.AnyProject {
		t.Fatalf("draft scope %+v %v %v", scope, ok, err)
	}
	if err = authz.RequirePattern(ctx, pattern, scope); err != nil {
		t.Fatal(err)
	}
	f.call(manager, "POST", "/api/recurrences/preview", f.input(), 200)
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
	for _, tc := range []struct{ start, due string }{{"now", "2026-10-24T23:30:00Z"}, {"hour", "2026-10-25T00:30:00Z"}, {"morning", "2026-10-26T05:00:00Z"}} {
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
