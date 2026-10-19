// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
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

func epicListAs(t *testing.T, f storageFixture, m *module, p tenant.Principal, query string) (GoalList, int, string) {
	t.Helper()
	r := goalRequest(t.Context(), m, p, "GET", "/api/goals/epics"+query, "", "")
	var out GoalList
	if r.Code == 200 {
		if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out, r.Code, r.Body.String()
}

func epicList(t *testing.T, f storageFixture, m *module, query string) (GoalList, int, string) {
	t.Helper()
	return epicListAs(t, f, m, f.reader, query)
}

func bindProjectRole(t *testing.T, tx pgx.Tx, tenantID, principal, project, roleKey string) {
	t.Helper()
	tag, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
		SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key=$4`, tenantID, principal, project, roleKey)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("bind %s: %v (%d)", roleKey, err, tag.RowsAffected())
	}
}

func epicByKey(t *testing.T, list GoalList, key string) GoalView {
	t.Helper()
	for _, g := range list.Items {
		if g.Key == key {
			return g
		}
	}
	t.Fatalf("missing %s in %d epics", key, len(list.Items))
	return GoalView{}
}

func epicItem(t *testing.T, g GoalView, key string) OutcomeView {
	t.Helper()
	for _, item := range g.Items {
		if len(item.Keys) > 0 && item.Keys[0] == key {
			return item
		}
	}
	t.Fatalf("missing item %s", key)
	return OutcomeView{}
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
		if _, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase,activity_note) VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',$5,$6,'working','Wiring the epics read')`, f.writer.TenantID, f.project, agent, live, []byte("epic-ref"), []byte("epic-lease")); err != nil {
			return err
		}
		// A principal has one binding per project, and the built-in guest role
		// cannot gain harness.read. Replace that binding with the guest set
		// plus harness.read. Workspace admin would also see EPC-13.
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'epic_live_read','Epic live read') RETURNING id::text`, f.writer.TenantID).Scan(&role); err != nil {
			return err
		}
		perms, ok := authz.BuiltinPermissions("guest")
		if !ok {
			return fmt.Errorf("guest permissions")
		}
		granted := append(append([]string{}, perms...), "harness.read")
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest($3::text[])`, f.writer.TenantID, role, granted); err != nil {
			return err
		}
		tag, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$3 WHERE tenant_id=$1 AND principal_id=$2 AND scope_type='project' AND scope_id=$4`, f.writer.TenantID, f.reader.ID, role, f.project)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("reader binding updated %d rows", tag.RowsAffected())
		}
		return nil
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

func seedReadyDefinition(t *testing.T, tx pgx.Tx, f storageFixture) error {
	t.Helper()
	var kind string
	if err := tx.QueryRow(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,allowed_child_kinds) VALUES($1,'definition','Definition','DEF','list',ARRAY[]::text[]) ON CONFLICT(tenant_id,slug) DO UPDATE SET slug=EXCLUDED.slug RETURNING id::text`, f.writer.TenantID).Scan(&kind); err != nil {
		return err
	}
	checks, err := json.Marshal(map[string]any{"checks": map[string]any{"in_progress": []map[string]any{{"id": "acceptance_present"}}}})
	if err != nil {
		return err
	}
	_, err = tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,fields) VALUES($1,$2,'DEF-2','Definition',$3,$4::jsonb)`, f.writer.TenantID, kind, f.project, checks)
	return err
}

// Risk: a project guest or a nodes.read key sees another project's live note,
// its author, session ids, pull request state or hold reason. Counts stay.
func TestEpicFix2RedactsNotesAuthorDeliveryAndHolds(t *testing.T) {
	f := writerFixture(t)
	var member tenant.Principal
	var narrow tenant.Principal
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		epic := epicFixtureNode(t, tx, f.writer.TenantID, "FX2-1", "Epic with a live item", "open", f.project, 1, nil)
		live := epicFixtureNode(t, tx, f.writer.TenantID, "FX2-2", "Live leaf", "in_progress", epic, 1, nil)
		epicFixtureNode(t, tx, f.writer.TenantID, "FX2-3", "Suggested leaf", "open", epic, 2, map[string]any{"suggested_release": "Next"})
		var worker string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Worker') RETURNING id::text`, f.writer.TenantID).Scan(&worker); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase,activity_note,heartbeat_at) VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',$5,$6,'working','Live note for the epic item',clock_timestamp())`, f.writer.TenantID, f.project, worker, live, []byte("fix2-ref"), []byte("fix2-lease")); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO delivery_items(tenant_id,id,project_id,ticket_node_id,repository,pull_request,state,state_since,owner,held_reason,observation,updated_at) VALUES($1,gen_random_uuid(),$2,$3,'aeon',41,'held',now(),'worker','waiting on review','{}'::jsonb,now())`, f.writer.TenantID, f.project, live); err != nil {
			return err
		}
		member = tenant.Principal{TenantID: f.writer.TenantID, Kind: tenant.Person, Name: "Epic member"}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Epic member') RETURNING id::text`, f.writer.TenantID).Scan(&member.ID); err != nil {
			return err
		}
		bindProjectRole(t, tx, f.writer.TenantID, member.ID, f.project, "member")
		narrow = tenant.Principal{TenantID: f.writer.TenantID, Kind: tenant.Agent, Name: "Narrow key", Scopes: []string{"nodes.read"}, KeyCreatorID: f.writer.ID}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Narrow key') RETURNING id::text`, f.writer.TenantID).Scan(&narrow.ID); err != nil {
			return err
		}
		bindProjectRole(t, tx, f.writer.TenantID, narrow.ID, f.project, "member")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &module{f.db.App, func() time.Time { return apiClock }}
	guest, code, body := epicList(t, f, m, "")
	if code != 200 {
		t.Fatalf("guest %d %s", code, body)
	}
	seen, code, body := epicListAs(t, f, m, member, "")
	if code != 200 {
		t.Fatalf("member %d %s", code, body)
	}
	hidden, code, body := epicListAs(t, f, m, narrow, "")
	if code != 200 {
		t.Fatalf("key %d %s", code, body)
	}
	assertLive := func(list GoalList, note bool, delivery bool) {
		t.Helper()
		g := epicByKey(t, list, "FX2-1")
		item := epicItem(t, g, "FX2-2")
		steps := strings.Join(func() []string {
			out := []string{}
			for _, s := range g.Steps {
				out = append(out, s.Text)
			}
			return out
		}(), " | ")
		if g.States == nil || g.States.Moving != 1 {
			t.Fatalf("moving count %+v", g.States)
		}
		if note {
			if item.Note != "Live note for the epic item" || item.NoteBy == "" || item.NoteByName != "Worker" || item.NoteAt == nil || len(item.SessionIDs) != 1 {
				t.Fatalf("expected live line %+v", item)
			}
		} else if item.Note != "" || item.NoteBy != "" || item.NoteByName != "" || item.NoteAt != nil || item.SessionIDs == nil || len(item.SessionIDs) != 0 {
			t.Fatalf("live line leaked %+v", item)
		}
		if delivery {
			if item.Delivery == nil || item.Delivery.State != "held" || !strings.Contains(steps, "FX2-2 on hold") || !strings.Contains(steps, "waiting on review") {
				t.Fatalf("expected hold %+v steps %q", item.Delivery, steps)
			}
		} else if item.Delivery != nil || strings.Contains(steps, "on hold") || strings.Contains(steps, "waiting on review") {
			t.Fatalf("delivery leaked %+v steps %q", item.Delivery, steps)
		}
		suggested := epicItem(t, g, "FX2-3")
		if suggested.ETA.Source != "release" || suggested.ETA.Text != "~ Next" {
			t.Fatalf("stored suggestion %+v", suggested.ETA)
		}
	}
	assertLive(seen, true, true)
	assertLive(guest, false, true)
	assertLive(hidden, false, false)
}

// Risk: Hide done drops an open epic once every child is done, and drops the
// failing checks that epic still has. The epic's own status is what hides it.
func TestEpicFix2OpenEpicStaysWithItsChecks(t *testing.T) {
	f := writerFixture(t)
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		if err := seedReadyDefinition(t, tx, f); err != nil {
			return err
		}
		epic := epicFixtureNode(t, tx, f.writer.TenantID, "OPN-1", "Open epic, done children", "open", f.project, 1, nil)
		epicFixtureNode(t, tx, f.writer.TenantID, "OPN-2", "Done child", "done", epic, 1, nil)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &module{f.db.App, func() time.Time { return apiClock }}
	list, code, body := epicList(t, f, m, "?hide_done=true")
	if code != 200 {
		t.Fatalf("epics %d %s", code, body)
	}
	g := epicByKey(t, list, "OPN-1")
	if g.Done == nil || *g.Done {
		t.Fatalf("open epic hidden as done: %+v", g.Done)
	}
	steps := []string{}
	for _, s := range g.Steps {
		steps = append(steps, s.Text)
	}
	if !strings.Contains(strings.Join(steps, " | "), "Acceptance criteria written") {
		t.Fatalf("checks dropped: %q", steps)
	}
	if g.NextStep == nil || g.NextStep.Text != "Acceptance criteria written" {
		t.Fatalf("next %+v", g.NextStep)
	}
}

// Risk: LANDS stops at the target release. A stored suggestion, and a queued
// leaf's visible wait on the release cadence, never become ~ Next. A key
// without releases.read still sees the stored field and not the queue.
func TestEpicFix2LandsUsesSuggestionAndQueue(t *testing.T) {
	f := writerFixture(t)
	var narrow tenant.Principal
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,allowed_child_kinds) VALUES($1,'work_order','Work order','WOR','work-order',ARRAY[]::text[]) ON CONFLICT(tenant_id,slug) DO NOTHING`, f.writer.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET allowed_child_kinds=CASE WHEN allowed_child_kinds IS NULL OR 'work_order'=ANY(allowed_child_kinds) THEN allowed_child_kinds ELSE array_append(allowed_child_kinds,'work_order') END WHERE tenant_id=$1 AND slug='work'`, f.writer.TenantID); err != nil {
			return err
		}
		epic := epicFixtureNode(t, tx, f.writer.TenantID, "LND-1", "Epic lands", "open", f.project, 1, map[string]any{"suggested_release": "Next"})
		epicFixtureNode(t, tx, f.writer.TenantID, "LND-2", "Stored suggestion", "open", epic, 1, map[string]any{"suggested_release": "Next"})
		queued := epicFixtureNode(t, tx, f.writer.TenantID, "LND-3", "Queued leaf", "open", epic, 2, map[string]any{"estimate_hours": 2})
		epicFixtureNode(t, tx, f.writer.TenantID, "LND-4", "Target release", "open", epic, 3, map[string]any{"release": "137", "suggested_release": "Next"})
		var order, runner string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,project_id) SELECT $1,id,'LND-5','Queued order',$2,$3 FROM node_kinds WHERE tenant_id=$1 AND slug='work_order' RETURNING id::text`, f.writer.TenantID, queued, f.project).Scan(&order); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES($1,$2,$3)`, f.writer.TenantID, order, f.writer.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Queue runner') RETURNING id::text`, f.writer.TenantID).Scan(&runner); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,queue_node_id,queue_by_principal_id,queue_at,queue_target_agent_id,queue_security_review_required) VALUES($1,$2,$3,$4,$5,now(),$3,false)`, f.writer.TenantID, order, runner, queued, f.writer.ID); err != nil {
			return err
		}
		narrow = tenant.Principal{TenantID: f.writer.TenantID, Kind: tenant.Agent, Name: "Narrow key", Scopes: []string{"nodes.read"}, KeyCreatorID: f.writer.ID}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Release-blind key') RETURNING id::text`, f.writer.TenantID).Scan(&narrow.ID); err != nil {
			return err
		}
		bindProjectRole(t, tx, f.writer.TenantID, narrow.ID, f.project, "member")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	prev := epicReleaseCadence
	epicReleaseCadence = func() (releaseGap, bool) {
		return releaseGap{hours: 24, latest: apiClock.Add(-12 * time.Hour)}, true
	}
	t.Cleanup(func() { epicReleaseCadence = prev })
	m := &module{f.db.App, func() time.Time { return apiClock }}
	guest, code, body := epicList(t, f, m, "")
	if code != 200 {
		t.Fatalf("guest %d %s", code, body)
	}
	g := epicByKey(t, guest, "LND-1")
	if g.ETA == nil || g.ETA.Source != "release" || g.ETA.Text != "~ Next" {
		t.Fatalf("epic lands %+v", g.ETA)
	}
	if a := epicItem(t, g, "LND-2"); a.ETA.Source != "release" || a.ETA.Text != "~ Next" {
		t.Fatalf("stored %+v", a.ETA)
	}
	if b := epicItem(t, g, "LND-3"); b.ETA.Source != "release" || b.ETA.Text != "~ Next" {
		t.Fatalf("queue %+v", b.ETA)
	}
	if c := epicItem(t, g, "LND-4"); c.ETA.Source != "release" || c.ETA.Text != "137" {
		t.Fatalf("target %+v", c.ETA)
	}
	blind, code, body := epicListAs(t, f, m, narrow, "")
	if code != 200 {
		t.Fatalf("key %d %s", code, body)
	}
	bg := epicByKey(t, blind, "LND-1")
	if bg.ETA == nil || bg.ETA.Text != "~ Next" {
		t.Fatalf("stored epic suggestion hidden %+v", bg.ETA)
	}
	if a := epicItem(t, bg, "LND-2"); a.ETA.Text != "~ Next" {
		t.Fatalf("stored item hidden %+v", a.ETA)
	}
	if b := epicItem(t, bg, "LND-3"); b.ETA.Source != "none" || b.ETA.Text != "" {
		t.Fatalf("queue leaked without releases.read %+v", b.ETA)
	}
	if c := epicItem(t, bg, "LND-4"); c.ETA.Text != "137" {
		t.Fatalf("target hidden %+v", c.ETA)
	}
}

func TestEpicFix2LandingText(t *testing.T) {
	now := apiClock
	gap := releaseGap{hours: 24, latest: now.Add(-12 * time.Hour)}
	if got := queueLandingText("open", 2, 0, true, gap, now); got != "~ Next" {
		t.Fatalf("next %q", got)
	}
	if got := queueLandingText("open", 30, 0, true, gap, now); got != "~ Next +1" {
		t.Fatalf("offset %q", got)
	}
	if got := queueLandingText("blocked", 2, 0, true, gap, now); got != "" {
		t.Fatalf("blocked %q", got)
	}
	if got := queueLandingText("open", 2, 0, false, gap, now); got != "" {
		t.Fatalf("unmeasured %q", got)
	}
}

// Risk: an untargeted queued leaf with no release and no stored suggestion
// shows no LANDS. The work list prefers the queue's expected start, and
// otherwise divides earlier estimates by the parallel-run capacity. A stored
// expected start 2000h after the epic clock is ~ Next +83. Behind a 65h
// blocked item and that 2h leaf, two parallel runs land the next leaf on
// ~ Next +1 only if the 2h is dropped, and on ~ Next +3 if the sum is not
// divided; the divided wait is ~ Next +2.
func TestEpicFix3UntargetedLandsAndExpectedStart(t *testing.T) {
	f := writerFixture(t)
	until := apiClock.Add(2000 * time.Hour)
	schedule := capacity.DefaultSchedule()
	schedule.Override = "hold"
	schedule.OverrideUntil = &until
	rawSchedule, err := json.Marshal(schedule)
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,allowed_child_kinds) VALUES($1,'work_order','Work order','WOR','work-order',ARRAY[]::text[]) ON CONFLICT(tenant_id,slug) DO NOTHING`, f.writer.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET allowed_child_kinds=CASE WHEN allowed_child_kinds IS NULL OR 'work_order'=ANY(allowed_child_kinds) THEN allowed_child_kinds ELSE array_append(allowed_child_kinds,'work_order') END WHERE tenant_id=$1 AND slug='work'`, f.writer.TenantID); err != nil {
			return err
		}
		epic := epicFixtureNode(t, tx, f.writer.TenantID, "FX3-1", "Epic pool", "open", f.project, 1, nil)
		start := epicFixtureNode(t, tx, f.writer.TenantID, "FX3-2", "Expected start", "open", epic, 1, map[string]any{"estimate_hours": 2})
		pool := epicFixtureNode(t, tx, f.writer.TenantID, "FX3-3", "Untargeted pool", "open", epic, 2, map[string]any{"estimate_hours": 2})
		ahead := epicFixtureNode(t, tx, f.writer.TenantID, "FX3-4", "Blocked ahead", "blocked", f.project, 2, map[string]any{"estimate_hours": 65})
		var runner, profile, account string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Pool runner') RETURNING id::text`, f.writer.TenantID).Scan(&runner); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'fx3-codex','1','codex','openai','fx3-model','high','standard') RETURNING id::text`, f.writer.TenantID).Scan(&profile); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,max_parallel_runs,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner) VALUES($1,'fx3pool','codex','fx3-daemon',$2,'Pool',2,clock_timestamp(),true,'fx3-generation',$3) RETURNING id::text`, f.writer.TenantID, runner, f.writer.ID).Scan(&account); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_use_cells(tenant_id,account_id,context_id,source,set_by) SELECT $1,$2,c.id,'person',$3 FROM work_contexts c WHERE c.tenant_id=$1 AND c.kind<>'holding' AND c.archived_at IS NULL ON CONFLICT DO NOTHING`, f.writer.TenantID, account, f.writer.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3::uuid,$4::jsonb)`, f.writer.TenantID, f.writer.ID, account, rawSchedule); err != nil {
			return err
		}
		queue := func(node, orderKey, age string, routed bool) error {
			var order string
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,project_id) SELECT $1,id,$2,'Queued order',$3,$4 FROM node_kinds WHERE tenant_id=$1 AND slug='work_order' RETURNING id::text`, f.writer.TenantID, orderKey, node, f.project).Scan(&order); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES($1,$2,$3)`, f.writer.TenantID, order, f.writer.ID); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,queue_node_id,queue_by_principal_id,queue_at,queue_routed_at,queue_security_review_required)
 VALUES($1,$2,$3,CASE WHEN $4 THEN $5::uuid END,$6,$7,clock_timestamp()-$8::interval,CASE WHEN $4 THEN clock_timestamp() END,false)`, f.writer.TenantID, order, runner, routed, profile, node, f.writer.ID, age)
			return err
		}
		if err := queue(ahead, "FX3-5", "3 hours", false); err != nil {
			return err
		}
		if err := queue(start, "FX3-6", "2 hours", true); err != nil {
			return err
		}
		return queue(pool, "FX3-7", "1 hours", false)
	})
	if err != nil {
		t.Fatal(err)
	}
	prev := epicReleaseCadence
	epicReleaseCadence = func() (releaseGap, bool) {
		return releaseGap{hours: 24, latest: apiClock.Add(-12 * time.Hour)}, true
	}
	t.Cleanup(func() { epicReleaseCadence = prev })
	m := &module{f.db.App, func() time.Time { return apiClock }}
	list, code, body := epicList(t, f, m, "")
	if code != 200 {
		t.Fatalf("epics %d %s", code, body)
	}
	g := epicByKey(t, list, "FX3-1")
	if a := epicItem(t, g, "FX3-2"); a.ETA.Source != "release" || a.ETA.Text != "~ Next +83" {
		t.Fatalf("expected start %+v", a.ETA)
	}
	if b := epicItem(t, g, "FX3-3"); b.ETA.Source != "release" || b.ETA.Text != "~ Next +2" {
		t.Fatalf("untargeted pool %+v", b.ETA)
	}
}

// Risk: the untargeted pool's divisor counts an account the account-use
// matrix denies for the leaf's project, and its wait sums another project's
// queue, so LANDS disagrees with the Work list's project view. Behind a 40h
// item in the same project, the 2-slot allowed account lands the 2h leaf on
// ~ Next +1. Adding the denied 8-slot account gives ~ Next; adding the other
// project's 400h gives ~ Next +9; both together give ~ Next +2.
func TestEpicFix4PoolCapacityFollowsProjectMatrix(t *testing.T) {
	f := writerFixture(t)
	var allowed, denied string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,allowed_child_kinds) VALUES($1,'work_order','Work order','WOR','work-order',ARRAY[]::text[]) ON CONFLICT(tenant_id,slug) DO NOTHING`, f.writer.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET allowed_child_kinds=CASE WHEN allowed_child_kinds IS NULL OR 'work_order'=ANY(allowed_child_kinds) THEN allowed_child_kinds ELSE array_append(allowed_child_kinds,'work_order') END WHERE tenant_id=$1 AND slug='work'`, f.writer.TenantID); err != nil {
			return err
		}
		var other string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Other project' FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='project' RETURNING id::text`, f.writer.TenantID).Scan(&other); err != nil {
			return err
		}
		// The reader sees the other project, so its entry is in the visible queue.
		bindProjectRole(t, tx, f.writer.TenantID, f.reader.ID, other, "guest")
		epic := epicFixtureNode(t, tx, f.writer.TenantID, "FX4-1", "Epic pool", "open", f.project, 1, nil)
		pool := epicFixtureNode(t, tx, f.writer.TenantID, "FX4-2", "Untargeted pool", "open", epic, 1, map[string]any{"estimate_hours": 2})
		ahead := epicFixtureNode(t, tx, f.writer.TenantID, "FX4-3", "Same project ahead", "blocked", f.project, 2, map[string]any{"estimate_hours": 40})
		elsewhere := epicFixtureNode(t, tx, f.writer.TenantID, "FX4-4", "Other project ahead", "blocked", other, 1, map[string]any{"estimate_hours": 400})
		var runner string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Pool runner') RETURNING id::text`, f.writer.TenantID).Scan(&runner); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'fx4-codex','1','codex','openai','fx4-model','high','standard')`, f.writer.TenantID); err != nil {
			return err
		}
		account := func(key string, slots int) (string, error) {
			var id string
			err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,max_parallel_runs,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner) VALUES($1,$2,'codex',$2,$3,$2,$4,clock_timestamp(),true,$2,$5) RETURNING id::text`, f.writer.TenantID, key, runner, slots, f.writer.ID).Scan(&id)
			return id, err
		}
		var err error
		if allowed, err = account("fx4allowed", 2); err != nil {
			return err
		}
		if denied, err = account("fx4denied", 8); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_use_cells(tenant_id,account_id,context_id,source,set_by) SELECT $1,$2,c.id,'person',$3 FROM work_contexts c WHERE c.tenant_id=$1 AND c.kind<>'holding' AND c.archived_at IS NULL ON CONFLICT DO NOTHING`, f.writer.TenantID, allowed, f.writer.ID); err != nil {
			return err
		}
		// A saved denial activates the matrix for the tenant.
		if _, err := tx.Exec(t.Context(), `DELETE FROM account_use_cells WHERE tenant_id=$1 AND account_id=$2`, f.writer.TenantID, denied); err != nil {
			return err
		}
		queue := func(node, project, orderKey, age string) error {
			var order string
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,project_id) SELECT $1,id,$2,'Queued order',$3,$4 FROM node_kinds WHERE tenant_id=$1 AND slug='work_order' RETURNING id::text`, f.writer.TenantID, orderKey, node, project).Scan(&order); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES($1,$2,$3)`, f.writer.TenantID, order, f.writer.ID); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,queue_node_id,queue_by_principal_id,queue_at,queue_security_review_required)
 VALUES($1,$2,$3,$4,$5,clock_timestamp()-$6::interval,false)`, f.writer.TenantID, order, runner, node, f.writer.ID, age)
			return err
		}
		if err := queue(ahead, f.project, "FX4-5", "4 hours"); err != nil {
			return err
		}
		if err := queue(elsewhere, other, "FX4-6", "3 hours"); err != nil {
			return err
		}
		return queue(pool, f.project, "FX4-7", "1 hours")
	})
	if err != nil {
		t.Fatal(err)
	}
	// The fixture must keep the matrix decision the assertion depends on.
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		var enforced, allowsAllowed, allowsDenied bool
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT enforced_at IS NOT NULL FROM account_use_rules),aeon_account_use_allowed_for_project($1,$3),aeon_account_use_allowed_for_project($2,$3)`, allowed, denied, f.project).Scan(&enforced, &allowsAllowed, &allowsDenied); err != nil {
			return err
		}
		if !enforced || !allowsAllowed || allowsDenied {
			t.Fatalf("matrix fixture enforced=%v allowed=%v denied=%v", enforced, allowsAllowed, allowsDenied)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	prev := epicReleaseCadence
	epicReleaseCadence = func() (releaseGap, bool) {
		return releaseGap{hours: 24, latest: apiClock.Add(-12 * time.Hour)}, true
	}
	t.Cleanup(func() { epicReleaseCadence = prev })
	m := &module{f.db.App, func() time.Time { return apiClock }}
	list, code, body := epicList(t, f, m, "")
	if code != 200 {
		t.Fatalf("epics %d %s", code, body)
	}
	if a := epicItem(t, epicByKey(t, list, "FX4-1"), "FX4-2"); a.ETA.Source != "release" || a.ETA.Text != "~ Next +1" {
		t.Fatalf("project pool %+v", a.ETA)
	}
}
