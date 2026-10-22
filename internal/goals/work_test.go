// SPDX-License-Identifier: AGPL-3.0-only

package goals

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type peopleGoal struct {
	ID    string
	Title string
	Items []struct {
		ID     string
		Ref    string
		Label  string
		Keys   []string
		Linked bool
	}
	Progress struct{ Pct, Total, Done int }
	ETA      struct {
		Source string
		At     *time.Time
	}
}

func readPeopleGoal(t *testing.T, r *httptest.ResponseRecorder) peopleGoal {
	t.Helper()
	if r.Code != 200 {
		t.Fatalf("goal read %d: %s", r.Code, r.Body)
	}
	var g peopleGoal
	if err := json.Unmarshal(r.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func readWork(t *testing.T, r *httptest.ResponseRecorder, status int) WorkResult {
	t.Helper()
	if r.Code != status {
		t.Fatalf("status %d want %d: %s", r.Code, status, r.Body)
	}
	var out WorkResult
	if status == 200 {
		if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func linkedLabels(g peopleGoal) []string {
	out := []string{}
	for _, item := range g.Items {
		if item.Linked {
			out = append(out, item.Label)
		}
	}
	slices.Sort(out)
	return out
}

// explicitOutcomes snapshots the agents' outcomes of a goal and what they serve.
func explicitOutcomes(t *testing.T, f storageFixture, goal string) string {
	t.Helper()
	var raw string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(jsonb_build_object('id',o.id,'title',o.title,'fields',o.fields,'deleted',o.deleted_at IS NOT NULL,'updated',o.updated_at,
 'serves',ARRAY(SELECT target_node_id FROM node_relations r WHERE r.source_node_id=o.id AND r.type='serves' ORDER BY target_node_id)) ORDER BY o.id),'[]')::text
 FROM nodes o WHERE o.tenant_id=$1 AND o.parent_id=$2 AND NOT o.fields @> '{"linked_work":true}'`, f.writer.TenantID, goal).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// Risk: people's Link work writes the wrong outcome or leaks work: a second
// implicit outcome, an agent outcome changed or wiped, a ticket the person
// cannot read linked, a retry that is not idempotent, progress that ignores
// links, unlink and its Undo, or a delete that touches tickets or cannot be
// undone. Permissions are the ones of creating a node: 403 otherwise.
func TestGoalPeopleCreateLinkUnlinkDelete(t *testing.T) {
	f := writerFixture(t)
	ctx := t.Context()
	tid := f.writer.TenantID
	member := tenant.Principal{TenantID: tid, Kind: tenant.Person}
	if err := f.db.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Goal member') RETURNING id::text`, tid).Scan(&member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, tid, member.ID, f.project); err != nil {
		t.Fatal(err)
	}
	var third, fourth string
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, tid, func(tx pgx.Tx) error {
		third = fixtureNode(t, tx, tid, "work", "TKT-3", f.project, map[string]any{"estimate_hours": 2})
		fourth = fixtureNode(t, tx, tid, "work", "TKT-4", f.project, map[string]any{"estimate_hours": 2})
		_, err := tx.Exec(ctx, `UPDATE nodes SET fields=fields||'{"estimate_hours":2}' WHERE id=$1`, f.ticket)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	m := &module{f.db.App, time.Now}
	calls := 0
	call := func(p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
		calls++
		return goalRequest(ctx, m, p, method, path, fmt.Sprintf("people-path-%d", calls), body)
	}

	// New goal: a project goal with Lands and a workspace goal where allowed, 403 where not.
	lands := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	body, _ := json.Marshal(map[string]any{"title": "People path", "project_id": f.project, "eta": map[string]any{"at": lands}})
	goal := readWriteResult(t, call(member, "POST", "/api/goals", string(body)), 200).ID
	readWriteResult(t, call(f.writer, "POST", "/api/goals", `{"title":"Whole workspace aim"}`), 200)
	readWriteResult(t, call(member, "POST", "/api/goals", `{"title":"Not mine to set"}`), 403)
	readWriteResult(t, call(f.reader, "POST", "/api/goals", `{"title":"Reader goal","project_id":"`+f.project+`"}`), 403)
	if g := readPeopleGoal(t, call(member, "GET", "/api/goals/"+goal, "")); g.ETA.Source != "agent" || g.ETA.At == nil || !g.ETA.At.Equal(lands) {
		t.Fatalf("created Lands %+v", g.ETA)
	}
	readWriteResult(t, call(member, "PATCH", "/api/goals/"+goal, `{"title":"People path, renamed"}`), 200)

	// Link three tickets; the retry changes nothing and keeps one implicit outcome.
	path := "/api/goals/" + goal + "/work"
	linked := readWork(t, call(member, "POST", path, `{"node_ids":["TKT-1","`+third+`","TKT-4","TKT-4"]}`), 200)
	if !linked.Changed || linked.EventID == nil || len(linked.NodeIDs) != 3 {
		t.Fatalf("link result %+v", linked)
	}
	again := readWork(t, call(member, "POST", path, `{"node_ids":["TKT-1","`+third+`","`+fourth+`"]}`), 200)
	if again.Changed || again.EventID != nil || !slices.Equal(again.NodeIDs, linked.NodeIDs) {
		t.Fatalf("repeated link %+v", again)
	}
	var implicit int
	if err := f.db.Admin.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE tenant_id=$1 AND parent_id=$2 AND fields @> '{"linked_work":true}'`, tid, goal).Scan(&implicit); err != nil || implicit != 1 {
		t.Fatalf("implicit outcomes %d, %v", implicit, err)
	}
	g := readPeopleGoal(t, call(member, "GET", "/api/goals/"+goal, ""))
	if g.Title != "People path, renamed" || !reflect.DeepEqual(linkedLabels(g), []string{"TKT-1", "TKT-3", "TKT-4"}) || len(g.Items) != 3 || g.Progress.Total != 3 || g.Progress.Done != 0 {
		t.Fatalf("linked goal %+v", g)
	}
	for _, item := range g.Items {
		if item.Ref != "" || len(item.Keys) != 1 || item.Keys[0] != item.Label {
			t.Fatalf("linked item names an outcome: %+v", item)
		}
	}
	if _, err := f.db.Admin.Exec(ctx, `UPDATE nodes SET state='done' WHERE tenant_id=$1 AND id=$2`, tid, third); err != nil {
		t.Fatal(err)
	}
	g = readPeopleGoal(t, call(member, "GET", "/api/goals/"+goal, ""))
	if g.Progress.Done != 1 || g.Progress.Pct != 33 {
		t.Fatalf("progress after one of three done %+v", g.Progress)
	}

	// Work the person cannot read, and a person who cannot write: 403, nothing changes.
	before := eventCount(t, f)
	readWork(t, call(member, "POST", path, `{"node_ids":["TKT-2"]}`), 403)
	readWork(t, call(member, "POST", path, `{"node_ids":["`+f.hiddenTicket+`","TKT-1"]}`), 403)
	readWork(t, call(f.reader, "POST", path, `{"node_ids":["TKT-1"]}`), 403)
	readWork(t, call(f.reader, "DELETE", path, `{"node_ids":["TKT-1"]}`), 403)
	if eventCount(t, f) != before {
		t.Fatal("a refused link appended events")
	}
	if g = readPeopleGoal(t, call(member, "GET", "/api/goals/"+goal, "")); len(g.Items) != 3 {
		t.Fatalf("a refused link changed the goal: %+v", g.Items)
	}

	// Unlink is idempotent; its Undo (the same link again) restores the rollup.
	unlinked := readWork(t, call(member, "DELETE", path, `{"node_ids":["TKT-3"]}`), 200)
	if !unlinked.Changed || len(unlinked.NodeIDs) != 2 || slices.Contains(unlinked.NodeIDs, third) {
		t.Fatalf("unlink %+v", unlinked)
	}
	if again = readWork(t, call(member, "DELETE", path, `{"node_ids":["TKT-3"]}`), 200); again.Changed || again.EventID != nil {
		t.Fatalf("repeated unlink %+v", again)
	}
	if g = readPeopleGoal(t, call(member, "GET", "/api/goals/"+goal, "")); g.Progress.Total != 2 || g.Progress.Done != 0 || g.Progress.Pct != 0 {
		t.Fatalf("progress after unlink %+v", g.Progress)
	}
	readWork(t, call(member, "POST", path, `{"node_ids":["TKT-3"]}`), 200)
	if g = readPeopleGoal(t, call(member, "GET", "/api/goals/"+goal, "")); g.Progress.Total != 3 || g.Progress.Done != 1 || g.Progress.Pct != 33 {
		t.Fatalf("progress after Undo %+v", g.Progress)
	}
	var list struct{ Items []peopleGoal }
	listed := call(member, "GET", "/api/goals?project=PRJ-1&limit=100", "")
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil || listed.Code != 200 {
		t.Fatalf("list %d %s", listed.Code, listed.Body)
	}
	found := false
	for _, item := range list.Items {
		if item.ID == goal {
			found = true
			if !reflect.DeepEqual(linkedLabels(item), []string{"TKT-1", "TKT-3", "TKT-4"}) || item.Progress.Pct != 33 {
				t.Fatalf("listed goal %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("linked goal missing from the list")
	}

	// An agent goal keeps its explicit outcomes: Link work adds to the implicit
	// one, and an agent's replace keeps the people's links.
	agentBefore := explicitOutcomes(t, f, f.goal)
	readWork(t, call(f.writer, "POST", "/api/goals/"+f.goal+"/work", `{"node_ids":["TKT-4"]}`), 200)
	if explicitOutcomes(t, f, f.goal) != agentBefore {
		t.Fatal("linking work changed an agent's outcome")
	}
	var latest int64
	if err := f.db.Admin.QueryRow(ctx, `SELECT max(id) FROM events WHERE tenant_id=$1 AND node_id=$2 AND type LIKE 'goal.%'`, tid, f.goal).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	replace, _ := json.Marshal(map[string]any{"if_read_through": latest, "items": map[string]any{"mode": "replace", "upsert": []map[string]any{{"ref": "visible"}, {"ref": "hidden"}, {"ref": "keyless"}}}})
	readWriteResult(t, call(f.writer, "PATCH", "/api/goals/"+f.goal, string(replace)), 200)
	agentGoal := readPeopleGoal(t, call(f.writer, "GET", "/api/goals/"+f.goal, ""))
	if !reflect.DeepEqual(linkedLabels(agentGoal), []string{"TKT-4"}) || len(agentGoal.Items) != 4 {
		t.Fatalf("agent goal after replace %+v", agentGoal.Items)
	}

	// Delete: never touches the tickets; its Undo restores the goal with its links.
	var tickets string
	snapshot := func() string {
		var raw string
		if err := f.db.Admin.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_object('id',id,'state',state,'fields',fields,'deleted',deleted_at IS NOT NULL,'updated',updated_at) ORDER BY id)::text FROM nodes WHERE tenant_id=$1 AND id=ANY($2::uuid[])`, tid, []string{f.ticket, third, fourth}).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	tickets = snapshot()
	readWork(t, call(f.reader, "DELETE", "/api/goals/"+goal, ""), 403)
	deleted := readWork(t, call(member, "DELETE", "/api/goals/"+goal, ""), 200)
	if deleted.EventID == nil {
		t.Fatal("delete returned no event to undo")
	}
	if r := call(member, "GET", "/api/goals/"+goal, ""); r.Code != 404 {
		t.Fatalf("deleted goal read %d", r.Code)
	}
	if snapshot() != tickets {
		t.Fatal("deleting a goal changed its linked tickets")
	}
	var e events.Event
	if err := f.db.Admin.QueryRow(ctx, `SELECT id,node_id::text,type,before FROM events WHERE tenant_id=$1 AND id=$2`, tid, *deleted.EventID).Scan(&e.ID, &e.NodeID, &e.Type, &e.Before); err != nil {
		t.Fatal(err)
	}
	undo := func() error {
		return db.InTenant(tenant.WithPrincipal(ctx, member), f.db.App, tid, func(tx pgx.Tx) error {
			change, err := UndoHandlers()[e.Type](tenant.WithPrincipal(ctx, member), tx, member, e)
			if err == nil && (change.Type != "goal.restored" || change.NodeID == nil || *change.NodeID != goal) {
				err = fmt.Errorf("compensating change %+v", change)
			}
			return err
		})
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if g = readPeopleGoal(t, call(member, "GET", "/api/goals/"+goal, "")); !reflect.DeepEqual(linkedLabels(g), []string{"TKT-1", "TKT-3", "TKT-4"}) {
		t.Fatalf("restored goal %+v", g.Items)
	}
	if err := undo(); !errors.Is(err, events.ErrConflict) {
		t.Fatalf("second undo of a live goal: %v", err)
	}
}

// latestGoalEvent is the newest event on a node: its type, its actor and that
// actor's name, so a test can say who made the change.
func latestGoalEvent(t *testing.T, f storageFixture, node string) (typ, actor, name string, before, after json.RawMessage) {
	t.Helper()
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT e.type,e.actor_principal_id::text,p.name,coalesce(e.before,'null'),coalesce(e.after,'null') FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id
 WHERE e.tenant_id=$1 AND e.node_id=$2 ORDER BY e.id DESC LIMIT 1`, f.writer.TenantID, node).Scan(&typ, &actor, &name, &before, &after); err != nil {
		t.Fatal(err)
	}
	return
}

// Risk (Tick spec v1.1, part a): a viewer or an API key without nodes.write
// renames, unlinks or deletes a goal; a refused write still appends an event
// or changes the goal; a change is recorded without the person who made it;
// an empty name is stored; Undo of a delete restores less than was there.
// The key with the scope is the positive control: the scope alone decides.
func TestGoalPeopleRenameUnlinkDeleteRights(t *testing.T) {
	f := writerFixture(t)
	ctx := t.Context()
	tid := f.writer.TenantID
	member := tenant.Principal{TenantID: tid, Kind: tenant.Person, Name: "Rename member"}
	narrow := tenant.Principal{TenantID: tid, Kind: tenant.Agent, Name: "Narrow key", Scopes: []string{"nodes.read"}}
	var fifth string
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Rename member') RETURNING id::text`, tid).Scan(&member.ID); err != nil {
			return err
		}
		bindProjectRole(t, tx, tid, member.ID, f.project, "member")
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Narrow key') RETURNING id::text`, tid).Scan(&narrow.ID); err != nil {
			return err
		}
		bindProjectRole(t, tx, tid, narrow.ID, f.project, "member")
		fifth = fixtureNode(t, tx, tid, "work", "TKT-5", f.project, nil)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	narrow.KeyCreatorID = member.ID
	scoped := narrow
	scoped.Scopes = []string{"nodes.read", "nodes.write"}
	m := &module{f.db.App, time.Now}
	calls := 0
	call := func(p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
		calls++
		return goalRequest(ctx, m, p, method, path, fmt.Sprintf("rights-%d", calls), body)
	}
	lands := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	body, _ := json.Marshal(map[string]any{"title": "Ship the people path", "project_id": f.project, "eta": map[string]any{"at": lands}})
	goal := readWriteResult(t, call(member, "POST", "/api/goals", string(body)), 200).ID
	path := "/api/goals/" + goal
	readWork(t, call(member, "POST", path+"/work", `{"node_ids":["TKT-1","TKT-5"]}`), 200)
	refused := func(what string, write func(p tenant.Principal) *httptest.ResponseRecorder) {
		t.Helper()
		before, shown := eventCount(t, f), readPeopleGoal(t, call(member, "GET", path, ""))
		for _, p := range []tenant.Principal{f.reader, narrow} {
			if r := write(p); r.Code != 403 {
				t.Fatalf("%s by %s: %d %s", what, p.Name, r.Code, r.Body)
			}
		}
		if eventCount(t, f) != before {
			t.Fatalf("a refused %s appended events", what)
		}
		if after := readPeopleGoal(t, call(member, "GET", path, "")); !reflect.DeepEqual(after, shown) {
			t.Fatalf("a refused %s changed the goal: %+v", what, after)
		}
	}

	// Rename: refused for the viewer and the narrow key, an empty name is refused,
	// and the event names the person and both names.
	refused("rename", func(p tenant.Principal) *httptest.ResponseRecorder {
		return call(p, "PATCH", path, `{"title":"Not yours to rename"}`)
	})
	before := eventCount(t, f)
	if r := call(member, "PATCH", path, `{"title":"   "}`); r.Code != 400 || eventCount(t, f) != before {
		t.Fatalf("empty name: %d %s", r.Code, r.Body)
	}
	readWriteResult(t, call(member, "PATCH", path, `{"title":"Ship the people path, renamed"}`), 200)
	typ, actor, name, was, now := latestGoalEvent(t, f, goal)
	var titles [2]struct{ Title string }
	if json.Unmarshal(was, &titles[0]) != nil || json.Unmarshal(now, &titles[1]) != nil || typ != "goal.renamed" || actor != member.ID || name != "Rename member" ||
		titles[0].Title != "Ship the people path" || titles[1].Title != "Ship the people path, renamed" {
		t.Fatalf("rename event %s by %s (%s): %s -> %s", typ, actor, name, was, now)
	}

	// Unlink: refused alike, allowed for the key with the scope; the person's
	// unlink is an event with their name.
	refused("unlink", func(p tenant.Principal) *httptest.ResponseRecorder {
		return call(p, "DELETE", path+"/work", `{"node_ids":["TKT-5"]}`)
	})
	if out := readWork(t, call(scoped, "DELETE", path+"/work", `{"node_ids":["TKT-5"]}`), 200); !out.Changed {
		t.Fatalf("scoped key unlink %+v", out)
	}
	if typ, actor, _, _, _ = latestGoalEvent(t, f, goal); typ != "goal.work_unlinked" || actor != narrow.ID {
		t.Fatalf("scoped key unlink event %s by %s", typ, actor)
	}
	readWork(t, call(member, "POST", path+"/work", `{"node_ids":["TKT-5"]}`), 200)
	unlinked := readWork(t, call(member, "DELETE", path+"/work", `{"node_ids":["TKT-5"]}`), 200)
	if !unlinked.Changed || slices.Contains(unlinked.NodeIDs, fifth) {
		t.Fatalf("unlink %+v", unlinked)
	}
	if typ, actor, name, _, _ = latestGoalEvent(t, f, goal); typ != "goal.work_unlinked" || actor != member.ID || name != "Rename member" {
		t.Fatalf("unlink event %s by %s (%s)", typ, actor, name)
	}
	readWork(t, call(member, "POST", path+"/work", `{"node_ids":["TKT-5"]}`), 200)

	// Delete: refused alike; the person's delete is an event with their name, and
	// Undo through the events API brings back the same goal: name, links, Lands, progress.
	refused("delete", func(p tenant.Principal) *httptest.ResponseRecorder { return call(p, "DELETE", path, "") })
	kept := readPeopleGoal(t, call(member, "GET", path, ""))
	if kept.Title != "Ship the people path, renamed" || !reflect.DeepEqual(linkedLabels(kept), []string{"TKT-1", "TKT-5"}) || kept.ETA.At == nil || !kept.ETA.At.Equal(lands) {
		t.Fatalf("goal before delete %+v", kept)
	}
	deleted := readWork(t, call(member, "DELETE", path, ""), 200)
	if typ, actor, name, _, _ = latestGoalEvent(t, f, goal); deleted.EventID == nil || typ != "goal.deleted" || actor != member.ID || name != "Rename member" {
		t.Fatalf("delete event %s by %s (%s), result %+v", typ, actor, name, deleted)
	}
	if r := call(member, "GET", path, ""); r.Code != 404 {
		t.Fatalf("deleted goal read %d", r.Code)
	}
	// The route's project scope resolves as the auth middleware does, so a
	// project member may undo the delete although the goal is deleted.
	const undoRoute = "POST /api/events/{eventId}/undo"
	undoPath := fmt.Sprintf("/api/events/%d/undo", *deleted.EventID)
	scope, ok, err := authz.ResolveRouteScope(tenant.WithPrincipal(ctx, member), f.db.App, undoRoute, undoPath)
	if err != nil || !ok || scope.ProjectID != f.project {
		t.Fatalf("undo route scope %+v ok=%v: %v", scope, ok, err)
	}
	undo := http.NewServeMux()
	events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(undo)
	req := httptest.NewRequest("POST", undoPath, nil).WithContext(authz.WithRouteScope(tenant.WithPrincipal(ctx, member), scope))
	rec := httptest.NewRecorder()
	undo.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("undo delete: %d %s", rec.Code, rec.Body)
	}
	if restored := readPeopleGoal(t, call(member, "GET", path, "")); !reflect.DeepEqual(restored, kept) {
		t.Fatalf("Undo restored %+v, want %+v", restored, kept)
	}
	if typ, actor, _, _, _ = latestGoalEvent(t, f, goal); typ != "goal.restored" || actor != member.ID {
		t.Fatalf("undo event %s by %s", typ, actor)
	}
}

// Risk: Undo of a link removes work another client had already linked, and
// creating the implicit outcome collides with a live or deleted explicit
// outcome whose ref is people-links.
func TestGoalWorkDeltaAndImplicitRef(t *testing.T) {
	f := writerFixture(t)
	ctx := t.Context()
	var third string
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		third = fixtureNode(t, tx, f.writer.TenantID, "work", "TKT-3", f.project, nil)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m := &module{f.db.App, time.Now}
	calls := 0
	call := func(method, path, body string) *httptest.ResponseRecorder {
		calls++
		return goalRequest(ctx, m, f.writer, method, path, fmt.Sprintf("delta-ref-%d", calls), body)
	}
	readWriteResult(t, call("PATCH", "/api/goals/"+f.emptyGoal, `{"items":{"upsert":[{"ref":"people-links","label":"Named by an agent","keys":["TKT-1"]}]}}`), 200)
	readWriteResult(t, call("PATCH", "/api/goals/"+f.secondGoal, `{"items":{"upsert":[{"ref":"people-links","label":"Retired name","keys":["TKT-1"]}]}}`), 200)
	readWriteResult(t, call("PATCH", "/api/goals/"+f.secondGoal, `{"items":{"remove":["people-links"]}}`), 200)
	liveBefore := explicitOutcomes(t, f, f.emptyGoal)
	deletedBefore := explicitOutcomes(t, f, f.secondGoal)

	path := "/api/goals/" + f.emptyGoal + "/work"
	first := readWork(t, call("POST", path, `{"node_ids":["TKT-1"]}`), 200)
	if !first.Changed || len(first.Delta) != 1 || len(first.NodeIDs) != 1 || first.Delta[0] != first.NodeIDs[0] {
		t.Fatalf("first link %+v", first)
	}
	mixed := readWork(t, call("POST", path, `{"node_ids":["TKT-1","`+third+`","TKT-1"]}`), 200)
	if !mixed.Changed || len(mixed.NodeIDs) != 2 || len(mixed.Delta) != 1 || mixed.Delta[0] != third || slices.Contains(mixed.Delta, first.Delta[0]) {
		t.Fatalf("mixed batch delta %+v, want only %s", mixed, third)
	}
	again := readWork(t, call("POST", path, `{"node_ids":["TKT-1","`+third+`"]}`), 200)
	if again.Changed || again.EventID != nil || len(again.Delta) != 0 || !slices.Equal(again.NodeIDs, mixed.NodeIDs) {
		t.Fatalf("repeated link %+v", again)
	}
	undone := readWork(t, call("DELETE", path, `{"node_ids":["`+mixed.Delta[0]+`"]}`), 200)
	if !undone.Changed || len(undone.Delta) != 1 || undone.Delta[0] != third || !slices.Equal(undone.NodeIDs, first.NodeIDs) {
		t.Fatalf("undoing the delta %+v dropped the pre-existing link %+v", undone, first.NodeIDs)
	}
	if got := explicitOutcomes(t, f, f.emptyGoal); got != liveBefore {
		t.Fatalf("live people-links outcome changed\nbefore %s\nafter %s", liveBefore, got)
	}
	beside := readWork(t, call("POST", "/api/goals/"+f.secondGoal+"/work", `{"node_ids":["`+third+`"]}`), 200)
	if !beside.Changed || len(beside.Delta) != 1 || beside.Delta[0] != third {
		t.Fatalf("link beside a deleted people-links outcome %+v", beside)
	}
	if got := explicitOutcomes(t, f, f.secondGoal); got != deletedBefore {
		t.Fatalf("deleted people-links outcome changed\nbefore %s\nafter %s", deletedBefore, got)
	}
	for _, goal := range []string{f.emptyGoal, f.secondGoal} {
		var count int
		var hasRef bool
		if err := f.db.Admin.QueryRow(ctx, `SELECT count(*), coalesce(bool_or(fields ? 'outcome_ref'), false) FROM nodes WHERE parent_id=$1 AND fields @> '{"linked_work":true}' AND deleted_at IS NULL`, goal).Scan(&count, &hasRef); err != nil {
			t.Fatal(err)
		}
		if count != 1 || hasRef {
			t.Fatalf("goal %s implicit outcomes=%d stores outcome_ref=%v", goal, count, hasRef)
		}
	}
}
