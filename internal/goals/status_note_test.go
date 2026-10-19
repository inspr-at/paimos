// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func noteWrite(t *testing.T, r *httptest.ResponseRecorder, status int) StatusNoteWriteResult {
	t.Helper()
	if r.Code != status {
		t.Fatalf("status %d want %d: %s", r.Code, status, r.Body)
	}
	var out StatusNoteWriteResult
	if status == 200 {
		if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func noteRead(t *testing.T, r *httptest.ResponseRecorder) *StatusNote {
	t.Helper()
	if r.Code != 200 {
		t.Fatalf("read %d %s", r.Code, r.Body)
	}
	var out StatusNoteResult
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Note
}

func goalList(t *testing.T, r *httptest.ResponseRecorder) GoalList {
	t.Helper()
	if r.Code != 200 {
		t.Fatalf("list %d %s", r.Code, r.Body)
	}
	var out GoalList
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// statusNoteEvents counts the note's events a principal can read under RLS.
func statusNoteEvents(t *testing.T, f storageFixture, p tenant.Principal) int {
	t.Helper()
	var n int
	err := db.InTenant(tenant.WithPrincipal(t.Context(), p), f.db.App, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type=$1 AND node_id IS NULL`, StatusNoteEventType).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Risk: the workspace status note is written by a project-only key, leaks to
// project readers or another tenant, or a retry/no-op appends a second event.
func TestGoalStatusNoteWriteReadScopeAndTenant(t *testing.T) {
	f := writerFixture(t)
	m := &module{f.db.App, func() time.Time { return apiClock }}
	path := "/api/goals/status-note"
	if got := noteRead(t, goalRequest(t.Context(), m, f.writer, "GET", path, "", "")); got != nil {
		t.Fatalf("empty workspace has a note %+v", got)
	}
	first := noteWrite(t, goalRequest(t.Context(), m, f.writer, "PATCH", path, "status-note-01", `{"text":"  Nine items merged since this morning.  "}`), 200)
	if !first.Changed || first.Note == nil || first.Note.Text != "Nine items merged since this morning." || first.Note.By != f.writer.ID || first.Note.ByName != "Goal fixture" || !first.Note.SetAt.Equal(apiClock) || first.ReadThrough == 0 {
		t.Fatalf("first write %+v", first)
	}
	if statusNoteEvents(t, f, f.writer) != 1 {
		t.Fatal("a changed note appends exactly one event")
	}
	later := &module{f.db.App, func() time.Time { return apiClock.Add(time.Hour) }}
	replay := noteWrite(t, goalRequest(t.Context(), later, f.writer, "PATCH", path, "status-note-01", `{"text":"  Nine items merged since this morning.  "}`), 200)
	if !reflect.DeepEqual(first, replay) {
		t.Fatalf("replay changed the result %+v", replay)
	}
	noteWrite(t, goalRequest(t.Context(), later, f.writer, "PATCH", path, "status-note-01", `{"text":"Another text"}`), 409)
	same := noteWrite(t, goalRequest(t.Context(), later, f.writer, "PATCH", path, "status-note-02", `{"text":"Nine items merged since this morning."}`), 200)
	if same.Changed || same.Note == nil || !same.Note.SetAt.Equal(apiClock) || statusNoteEvents(t, f, f.writer) != 1 {
		t.Fatalf("repeating the text moved attribution or wrote an event %+v", same)
	}
	for _, body := range []string{`{}`, `{"text":""}`, `{"text":"   "}`, `{"text":"` + strings.Repeat("a", MaxStatusNoteRunes+1) + `"}`, `{"text":"x","extra":1}`} {
		noteWrite(t, goalRequest(t.Context(), m, f.writer, "PATCH", path, "status-note-bad", body), 400)
	}

	// The project guest reads goals in its project, never the workspace note.
	if r := goalRequest(t.Context(), m, f.reader, "GET", path, "", ""); r.Code != 403 {
		t.Fatalf("project reader read the workspace note: %d %s", r.Code, r.Body)
	}
	if list := goalList(t, goalRequest(t.Context(), m, f.reader, "GET", "/api/goals", "", "")); list.StatusNote != nil {
		t.Fatalf("project reader's list carries the note %+v", list.StatusNote)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='member') WHERE tenant_id=$1 AND principal_id=$2`, f.reader.TenantID, f.reader.ID); err != nil {
		t.Fatal(err)
	}
	noteWrite(t, goalRequest(t.Context(), m, f.reader, "PATCH", path, "status-note-03", `{"text":"Project writer"}`), 403)
	if statusNoteEvents(t, f, f.reader) != 0 {
		t.Fatal("the note's event reached a project-only reader")
	}
	if list := goalList(t, goalRequest(t.Context(), m, f.writer, "GET", "/api/goals", "", "")); list.StatusNote == nil || list.StatusNote.Text != first.Note.Text {
		t.Fatalf("workspace reader's list lacks the note %+v", list.StatusNote)
	}

	// Another tenant has its own (empty) note and cannot overwrite this one.
	other := tenant.Principal{Kind: tenant.Person}
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('goals-note-other','Other') RETURNING id::text`).Scan(&other.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Other admin') RETURNING id::text`, other.TenantID).Scan(&other.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.db, other.TenantID, other.ID, "admin")
	if got := noteRead(t, goalRequest(t.Context(), m, other, "GET", path, "", "")); got != nil {
		t.Fatalf("tenant isolation: %+v", got)
	}
	noteWrite(t, goalRequest(t.Context(), m, other, "PATCH", path, "status-note-01", `{"text":"Other tenant"}`), 200)
	if got := noteRead(t, goalRequest(t.Context(), m, f.writer, "GET", path, "", "")); got == nil || got.Text != first.Note.Text {
		t.Fatalf("another tenant changed this note %+v", got)
	}

	cleared := noteWrite(t, goalRequest(t.Context(), later, f.writer, "PATCH", path, "status-note-04", `{"text":null}`), 200)
	if !cleared.Changed || cleared.Note != nil || noteRead(t, goalRequest(t.Context(), m, f.writer, "GET", path, "", "")) != nil || statusNoteEvents(t, f, f.writer) != 2 {
		t.Fatalf("clear %+v", cleared)
	}
	again := noteWrite(t, goalRequest(t.Context(), later, f.writer, "PATCH", path, "status-note-05", `{"text":null}`), 200)
	if again.Changed || statusNoteEvents(t, f, f.writer) != 2 {
		t.Fatal("clearing nothing wrote an event")
	}
}

// Risk: an item note loses its author/time or re-stamps them on a no-op;
// hours left counts finished work, double counts shared leaves or hides
// unestimated leaves; "today" and freshness read the wrong timestamps.
func TestGoalItemNoteHoursLeftMergedAndFreshness(t *testing.T) {
	f := writerFixture(t)
	merged := apiClock.Add(-2 * time.Hour)
	var open, unestimated string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		open = fixtureNode(t, tx, f.writer.TenantID, "work", "LEAF-11", f.ticket, map[string]any{"estimate_hours": 3})
		unestimated = fixtureNode(t, tx, f.writer.TenantID, "work", "LEAF-12", f.ticket, nil)
		finished := fixtureNode(t, tx, f.writer.TenantID, "work", "DONE-1", f.project, map[string]any{"estimate_hours": 5, "merged_at": merged.Format(time.RFC3339Nano)})
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='done' WHERE id=$1`, finished)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/goals/" + f.secondGoal
	// Two open items share TKT-1's leaves; the third is finished by a merge.
	readWriteResult(t, apiRequest(t, f, "PATCH", path, "gaps-items-01", `{"items":{"upsert":[{"ref":"a","label":"A","keys":["TKT-1"]},{"ref":"shared","label":"Shared","keys":["TKT-1"]},{"ref":"b","label":"B","keys":["DONE-1"]}]}}`), 200)
	readWriteResult(t, apiRequest(t, f, "PATCH", path, "gaps-note-01", `{"items":{"upsert":[{"ref":"a","note":"Hand-check of 5 review runs first"}]}}`), 200)
	later := &module{f.db.App, func() time.Time { return apiClock.Add(time.Hour) }}
	readWriteResult(t, goalRequest(t.Context(), later, f.writer, "PATCH", path, "gaps-note-02", `{"items":{"upsert":[{"ref":"a","note":"Hand-check of 5 review runs first"},{"ref":"shared","eta":{"text":"tonight"}}]}}`), 200)

	g := detail(t, goalRequest(t.Context(), later, f.writer, "GET", path, "", ""))
	items := map[string]OutcomeView{}
	for _, item := range g.Items {
		items[item.Ref] = item
	}
	a, b := items["a"], items["b"]
	if a.Note != "Hand-check of 5 review runs first" || a.NoteBy != f.writer.ID || a.NoteByName != "Goal fixture" || a.NoteAt == nil || !a.NoteAt.Equal(apiClock) {
		t.Fatalf("item note attribution (a no-op must keep the first time) %+v", a)
	}
	if b.State != Done || b.MergedAt == nil || !b.MergedAt.Equal(merged) || a.MergedAt != nil {
		t.Fatalf("merged_at on the done item only: a=%v b=%+v", a.MergedAt, b)
	}
	if g.Progress == nil || g.Progress.RemainingHours != 3 || g.Progress.RemainingUnestimated != 1 {
		t.Fatalf("hours left over open leaves, each once %+v", g.Progress)
	}
	list := goalList(t, goalRequest(t.Context(), later, f.writer, "GET", "/api/goals", "", ""))
	if list.ETALastSetAt == nil || !list.ETALastSetAt.Equal(apiClock.Add(time.Hour)) {
		t.Fatalf("freshness is the latest authored ETA %v", list.ETALastSetAt)
	}
	for _, listed := range list.Items {
		if listed.ID != f.secondGoal {
			continue
		}
		if listed.Progress == nil || listed.Progress.RemainingHours != 3 || listed.Progress.RemainingUnestimated != 1 {
			t.Fatalf("list and detail disagree on hours left %+v", listed.Progress)
		}
		for _, item := range listed.Items {
			if item.Ref == "a" && (item.NoteByName != "Goal fixture" || item.NoteAt == nil) || item.Ref == "b" && (item.MergedAt == nil || !item.MergedAt.Equal(merged)) {
				t.Fatalf("list item %+v", item)
			}
		}
	}

	// Finishing a leaf moves its hours out; clearing the note clears its author.
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET state='done' WHERE tenant_id=$1 AND id=ANY($2::uuid[])`, f.writer.TenantID, []string{open}); err != nil {
		t.Fatal(err)
	}
	readWriteResult(t, apiRequest(t, f, "PATCH", path, "gaps-note-03", `{"items":{"upsert":[{"ref":"a","note":null}]}}`), 200)
	g = detail(t, goalRequest(t.Context(), later, f.writer, "GET", path, "", ""))
	if g.Progress.RemainingHours != 0 || g.Progress.RemainingUnestimated != 1 {
		t.Fatalf("finished leaf still counted %+v (open unestimated %s)", g.Progress, unestimated)
	}
	for _, item := range g.Items {
		if item.Ref == "a" && (item.Note != "" || item.NoteBy != "" || item.NoteAt != nil) {
			t.Fatalf("cleared note kept attribution %+v", item)
		}
	}
}

// Risk: a goal shows a percent that is not its own items' rollup (another
// goal's, or one that ignores a finished item), or a hand-set percent that a
// reader cannot tell from a computed one. Tick's early read of the Goals view
// saw "0 of 1 done" beside 31 % and no pen.
func TestGoalPercentComputedUnlessHandSet(t *testing.T) {
	f := writerFixture(t)
	a, b := "/api/goals/"+f.secondGoal, "/api/goals/"+f.emptyGoal
	readWriteResult(t, apiRequest(t, f, "PATCH", a, "pct-items-a", `{"items":{"upsert":[{"ref":"a1","label":"A1","keys":["TKT-1"],"state":"done"},{"ref":"a2","label":"A2","keys":["TKT-1"],"state":"queued"}]}}`), 200)
	readWriteResult(t, apiRequest(t, f, "PATCH", b, "pct-items-b", `{"items":{"upsert":[{"ref":"b1","label":"B1","keys":["TKT-1"],"state":"queued"}]}}`), 200)
	// Detail and list must agree; the returned map holds each goal's progress.
	read := func(stage string) map[string]GoalProgress {
		t.Helper()
		out := map[string]GoalProgress{}
		for id, path := range map[string]string{f.secondGoal: a, f.emptyGoal: b} {
			g := detail(t, apiRequest(t, f, "GET", path, "", ""))
			if g.Progress == nil {
				t.Fatalf("%s: goal %s without progress", stage, id)
			}
			out[id] = *g.Progress
		}
		listed := 0
		for _, g := range goalList(t, apiRequest(t, f, "GET", "/api/goals", "", "")).Items {
			if want, ok := out[g.ID]; ok {
				listed++
				if g.Progress == nil || !reflect.DeepEqual(*g.Progress, want) {
					t.Fatalf("%s: list and detail disagree for %s: %+v vs %+v", stage, g.ID, g.Progress, want)
				}
			}
		}
		if listed != 2 {
			t.Fatalf("%s: list carried %d of the 2 goals", stage, listed)
		}
		return out
	}
	computed := func(stage string, p GoalProgress, pct, done, total int) {
		t.Helper()
		if p.Source != "computed" || p.Pct != pct || p.ComputedPct != pct || p.Done != done || p.Total != total || p.OverrideBy != "" || p.OverrideNote != "" || p.OverrideAt != "" {
			t.Fatalf("%s: want computed %d %% (%d of %d) without override fields, got %+v", stage, pct, done, total, p)
		}
	}
	handSet := func(stage string, p GoalProgress, pct, computedPct int) {
		t.Helper()
		if p.Source != "override" || p.Pct != pct || p.ComputedPct != computedPct || p.OverrideBy != f.writer.ID || p.OverrideNote != "agreed at the cut" || p.OverrideAt == "" {
			t.Fatalf("%s: want hand-set %d %% over computed %d %%, got %+v", stage, pct, computedPct, p)
		}
	}

	got := read("computed")
	computed("computed A", got[f.secondGoal], 50, 1, 2)
	computed("computed B", got[f.emptyGoal], 0, 0, 1)

	// Hand-set on B only: B says so and keeps tracking its items underneath; A is untouched.
	readWriteResult(t, apiRequest(t, f, "PATCH", b, "pct-hand-b", `{"progress_override":{"pct":31,"note":"agreed at the cut"}}`), 200)
	got = read("hand-set")
	computed("hand-set A", got[f.secondGoal], 50, 1, 2)
	handSet("hand-set B", got[f.emptyGoal], 31, 0)
	readWriteResult(t, apiRequest(t, f, "PATCH", b, "pct-done-b", `{"items":{"upsert":[{"ref":"b1","state":"done"}]}}`), 200)
	handSet("hand-set B, item done", read("hand-set, item done")[f.emptyGoal], 31, 100)

	// Cleared: B's percent is its items' rollup again.
	readWriteResult(t, apiRequest(t, f, "PATCH", b, "pct-clear-b", `{"progress_override":null}`), 200)
	got = read("cleared")
	computed("cleared A", got[f.secondGoal], 50, 1, 2)
	computed("cleared B", got[f.emptyGoal], 100, 1, 1)
}
