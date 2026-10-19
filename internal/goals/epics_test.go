// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func epicFixtureNode(t *testing.T, tx pgx.Tx, tenantID, key, title, state, parent string, position int, fields map[string]any) string {
	t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,parent_id,position,fields)
		SELECT $1,id,$2,$3,$4,$5,$6,$7::jsonb FROM node_kinds WHERE tenant_id=$1 AND slug='work' RETURNING id::text`,
		tenantID, key, title, state, parent, position, raw).Scan(&id); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
	return id
}

func epicList(t *testing.T, f storageFixture, m *module, query string) (GoalList, int, string) {
	t.Helper()
	r := goalRequest(t.Context(), m, f.reader, "GET", "/api/goals/epics"+query, "", "")
	var out GoalList
	if r.Code == 200 {
		if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out, r.Code, r.Body.String()
}

// Risk (AEON-1395): Goals › Epics lists nested packages, closed or invisible
// epics as areas; counts ignore the children's status or live work; progress
// counts a parent's own estimate or unestimated leaves as hours; "what's
// missing" lists checks out of definition order, passing ones, or a heading
// for an epic with none; a page cursor crosses between Goals and Epics.
func TestGoalEpicsAreasRollupAndMissingChecks(t *testing.T) {
	f := writerFixture(t)
	var first, second, packageID, agent string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		var kind string
		if err := tx.QueryRow(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,allowed_child_kinds) VALUES($1,'definition','Definition','DEF','list',ARRAY[]::text[]) ON CONFLICT(tenant_id,slug) DO UPDATE SET slug=EXCLUDED.slug RETURNING id::text`, f.writer.TenantID).Scan(&kind); err != nil {
			return err
		}
		// Ready: acceptance first, then a person's step, then the estimate; QA has no checks.
		checks, _ := json.Marshal(map[string]any{"checks": map[string]any{"in_progress": []map[string]any{
			{"id": "acceptance_present"}, {"id": "manual:release-notes", "label": "Release notes reviewed", "owner_id": f.writer.ID}, {"id": "estimate_set"},
		}}})
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,fields) VALUES($1,$2,'DEF-1','Definition',$3,$4::jsonb)`, f.writer.TenantID, kind, f.project, checks); err != nil {
			return err
		}
		first = epicFixtureNode(t, tx, f.writer.TenantID, "EPC-1", "Epics in the goal shape", "open", f.project, 1, map[string]any{"release": "136"})
		epicFixtureNode(t, tx, f.writer.TenantID, "EPC-2", "Done leaf", "done", first, 1, map[string]any{"estimate_hours": 3})
		// The package's own 50 h never count: only its leaves do.
		packageID = epicFixtureNode(t, tx, f.writer.TenantID, "EPC-3", "Server rollup package", "in_progress", first, 2, map[string]any{"estimate_hours": 50})
		epicFixtureNode(t, tx, f.writer.TenantID, "EPC-4", "Done sub-leaf", "done", packageID, 1, map[string]any{"estimate_hours": 1})
		live := epicFixtureNode(t, tx, f.writer.TenantID, "EPC-5", "Open sub-leaf", "in_progress", packageID, 2, map[string]any{"estimate_hours": 4})
		epicFixtureNode(t, tx, f.writer.TenantID, "EPC-6", "Parked leaf", "backlog", first, 3, nil)
		epicFixtureNode(t, tx, f.writer.TenantID, "EPC-7", "Queued leaf", "open", first, 4, map[string]any{"release": "137"})
		epicFixtureNode(t, tx, f.writer.TenantID, "EPC-8", "Cancelled leaf", "cancelled", first, 5, map[string]any{"estimate_hours": 9})
		second = epicFixtureNode(t, tx, f.writer.TenantID, "EPC-9", "Epic with nothing missing", "in_progress", f.project, 2, nil)
		epicFixtureNode(t, tx, f.writer.TenantID, "EPC-10", "Its moving leaf", "in_progress", second, 1, map[string]any{"estimate_hours": 2})
		closed := epicFixtureNode(t, tx, f.writer.TenantID, "EPC-11", "Closed epic", "done", f.project, 3, nil)
		epicFixtureNode(t, tx, f.writer.TenantID, "EPC-12", "Its done leaf", "done", closed, 1, nil)
		hidden := epicFixtureNode(t, tx, f.writer.TenantID, "EPC-13", "Invisible epic", "open", f.otherProject, 1, nil)
		epicFixtureNode(t, tx, f.writer.TenantID, "EPC-14", "Invisible leaf", "open", hidden, 1, nil)
		if _, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES ($1,$2,$3,'blocks')`, f.writer.TenantID, f.ticket, first); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Worker') RETURNING id::text`, f.writer.TenantID).Scan(&agent); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase,activity_note) VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',$5,$6,'working','Wiring the epics read')`, f.writer.TenantID, f.project, agent, live, []byte("epic-ref"), []byte("epic-lease"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &module{f.db.App, func() time.Time { return apiClock }}
	list, code, body := epicList(t, f, m, "?sort=name")
	if code != 200 {
		t.Fatalf("epics %d %s", code, body)
	}
	keys := []string{}
	for _, g := range list.Items {
		keys = append(keys, g.Key)
	}
	// Only the topmost open parents the reader may see: no package, closed or invisible epic, no leaf.
	if !reflect.DeepEqual(keys, []string{"EPC-9", "EPC-1"}) {
		t.Fatalf("areas %v", keys)
	}
	epic, quiet := list.Items[1], list.Items[0]
	if epic.Kind != "epic" || epic.ProjectID == nil || *epic.ProjectID != f.project || len(epic.Projects) != 1 || epic.Projects[0].ID != f.project || epic.Expansion != "full" {
		t.Fatalf("epic identity %+v", epic)
	}
	// Items are the direct children without the cancelled one; the package's keys follow its own.
	items := map[string]OutcomeView{}
	for _, item := range epic.Items {
		items[item.Keys[0]] = item
	}
	if len(epic.Items) != 4 || epic.Counts.Outcomes != 4 || epic.Counts.Tickets != 5 {
		t.Fatalf("items %+v counts %+v", epic.Items, epic.Counts)
	}
	if got := items["EPC-3"].Keys; !reflect.DeepEqual(got, []string{"EPC-3", "EPC-4", "EPC-5"}) {
		t.Fatalf("package keys %v", got)
	}
	for key, want := range map[string]State{"EPC-2": Done, "EPC-3": Moving, "EPC-6": Paused, "EPC-7": Queued} {
		if items[key].State != want {
			t.Fatalf("%s state %s, want %s", key, items[key].State, want)
		}
	}
	if epic.States == nil || *epic.States != (StateCounts{Moving: 1, Queued: 1, Paused: 1, Done: 1}) || epic.Done == nil || *epic.Done {
		t.Fatalf("counts %+v done %v", epic.States, epic.Done)
	}
	// The live session moves its package and is the item's live line.
	pkg := items["EPC-3"]
	if len(pkg.SessionIDs) != 1 || pkg.Note != "Wiring the epics read" || pkg.NoteBy != agent || pkg.NoteByName != "Worker" {
		t.Fatalf("package live line %+v", pkg)
	}
	// Leaf hours only: (3 + 1) done of 3 + 1 + 4 estimated; the package's 50 h and the cancelled 9 h never count.
	p := epic.Progress
	if p == nil || p.Pct != 50 || p.ComputedPct != 50 || p.Source != "computed" || p.Basis != "mixed" || p.Total != 4 || p.Done != 1 || p.RemainingHours != 4 || p.RemainingUnestimated != 2 {
		t.Fatalf("progress %+v", p)
	}
	// LANDS: no worker ETA yet, so the epic's target release; an item keeps its own.
	if epic.ETA == nil || epic.ETA.Source != "release" || epic.ETA.Text != "136" || items["EPC-7"].ETA.Source != "release" || items["EPC-7"].ETA.Text != "137" {
		t.Fatalf("eta %+v item %+v", epic.ETA, items["EPC-7"].ETA)
	}
	// What's missing: the failing Ready checks in definition order, then the blocker.
	steps := []string{}
	for _, s := range epic.Steps {
		steps = append(steps, s.State+":"+s.Text)
	}
	want := []string{"now:Acceptance criteria written", "later:Waiting for Goal fixture: Release notes reviewed", "later:Estimate set", "later:Blocked by TKT-1 TKT-1"}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps %q", steps)
	}
	if epic.NextStep == nil || epic.NextStep.Text != "Server rollup package" {
		t.Fatalf("next %+v", epic.NextStep)
	}
	// In progress with no QA checks: nothing is missing, no steps at all; Next is its open item.
	if len(quiet.Steps) != 0 || quiet.NextStep == nil || quiet.NextStep.Text != "Its moving leaf" || quiet.Progress == nil || quiet.Progress.Pct != 0 || quiet.Progress.Basis != "estimate" {
		t.Fatalf("quiet epic %+v next %+v progress %+v", quiet.Steps, quiet.NextStep, quiet.Progress)
	}
	// The chips count items over every area, whatever the page shows.
	if list.Totals != (StateCounts{Moving: 2, Queued: 1, Paused: 1, Done: 1}) || list.TotalsPartial {
		t.Fatalf("totals %+v", list.Totals)
	}
	if found, code, _ := epicList(t, f, m, "?q=epc-9"); code != 200 || len(found.Items) != 1 || found.Items[0].ID != second {
		t.Fatalf("search by key %d %+v", code, found.Items)
	}

	// Pages: one epic per page, the cursor continues; a Goals cursor is refused here and back.
	page, code, body := epicList(t, f, m, "?sort=name&limit=1")
	if code != 200 || len(page.Items) != 1 || page.Items[0].ID != second || page.NextCursor == "" {
		t.Fatalf("first page %d %s", code, body)
	}
	rest, code, body := epicList(t, f, m, "?sort=name&limit=1&cursor="+url.QueryEscape(page.NextCursor))
	if code != 200 || len(rest.Items) != 1 || rest.Items[0].ID != first {
		t.Fatalf("second page %d %s", code, body)
	}
	goals := listResult(t, f, m, "?sort=name&limit=1")
	if goals.NextCursor == "" {
		t.Fatal("goals fixture has one page only")
	}
	if _, code, body := epicList(t, f, m, "?sort=name&limit=1&cursor="+url.QueryEscape(goals.NextCursor)); code != 400 || !strings.Contains(body, "filters changed") {
		t.Fatalf("goals cursor on epics %d %s", code, body)
	}
	if r := goalRequest(t.Context(), m, f.reader, "GET", "/api/goals?sort=name&limit=1&cursor="+url.QueryEscape(page.NextCursor), "", ""); r.Code != 400 {
		t.Fatalf("epics cursor on goals %d %s", r.Code, r.Body)
	}
}

// Risk: the item states drift from the AEON-1395 mapping: a live session must
// beat a done status, In progress and QA move, Backlog and paused work pause,
// and an empty open item stays Queued with its reason.
func TestEpicItemStateMapping(t *testing.T) {
	leaf := func(f leafFact) []leafFact { return []leafFact{f} }
	for _, tc := range []struct {
		name, category, state string
		leaves                []leafFact
		want                  State
		reason                string
	}{
		{"live session beats done", "done", "done", leaf(leafFact{ID: "a", Done: true, Sessions: []string{"s"}}), Moving, ""},
		{"delivered is done", "delivered", "delivered", nil, Done, ""},
		{"every leaf done", "in_progress", "in_progress", leaf(leafFact{ID: "a", Done: true}), Done, ""},
		{"in progress moves", "in_progress", "in_progress", leaf(leafFact{ID: "a"}), Moving, ""},
		{"qa moves", "in_progress", "QA", leaf(leafFact{ID: "a"}), Moving, ""},
		{"open pull request moves", "open", "open", leaf(leafFact{ID: "a", PR: true}), Moving, ""},
		{"backlog pauses", "open", "Backlog", leaf(leafFact{ID: "a"}), Paused, ""},
		{"paused on purpose", "open", "open", leaf(leafFact{ID: "a", Paused: true}), Paused, ""},
		{"queued for a start", "open", "open", leaf(leafFact{ID: "a", Queued: true}), Queued, "start_queue"},
		{"blocked", "blocked", "blocked", leaf(leafFact{ID: "a", Blocked: true}), Queued, "blocked"},
		{"open waits", "open", "new", nil, Queued, "waiting"},
	} {
		got := EpicItemState(tc.category, tc.state, tc.leaves)
		if got.State != tc.want || got.Reason != tc.reason || got.SessionIDs == nil {
			t.Errorf("%s: got %+v, want %s %q", tc.name, got, tc.want, tc.reason)
		}
	}
	if next := []string{EpicNextStatus("open", "open"), EpicNextStatus("blocked", "blocked"), EpicNextStatus("in_progress", "in_progress"), EpicNextStatus("in_progress", "qa"), EpicNextStatus("done", "done")}; !reflect.DeepEqual(next, []string{"in_progress", "in_progress", "qa", "done", ""}) {
		t.Fatalf("next statuses %v", next)
	}
}
