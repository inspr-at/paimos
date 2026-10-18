// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/events"
)

func TestBulkChangeSkipsWithReasonsAndUndoesAsOne(t *testing.T) {
	p := newPrincipal(t, "bulk")
	customKind(t, p, "epic", "epic")
	customKind(t, p, "task", "task")
	project, epic, ticket, task := kindBySlug(t, p, "project"), kindBySlug(t, p, "epic"), kindBySlug(t, p, "work"), kindBySlug(t, p, "task")
	create := func(kind, key, state, parent string, fields string) nodeJSON {
		t.Helper()
		if kind == ticket.ID {
			var values map[string]any
			_ = json.Unmarshal([]byte(benefitFields), &values)
			_ = json.Unmarshal([]byte(fields), &values)
			raw, _ := json.Marshal(values)
			fields = string(raw)
		}
		body := `{"kind_id":"` + kind + `","key":"` + key + `","title":"` + key + `","state":"` + state + `","fields":` + fields
		if parent != "" {
			body += `,"parent_id":"` + parent + `"`
		}
		return mustNode(t, p, body+`}`)
	}
	root := create(project.ID, "PRJ-1", "active", "", `{}`)
	other := create(project.ID, "PRJ-2", "active", "", `{}`)
	e1 := create(epic.ID, "PAI-1", "backlog", root.ID, `{}`)
	e2 := create(epic.ID, "PAI-2", "backlog", root.ID, `{}`)
	foreignEpic := create(epic.ID, "OTH-1", "backlog", other.ID, `{}`)
	a := create(ticket.ID, "PAI-3", "new", e1.ID, `{"priority":"high","assignee":"`+p.ID+`","tags":[{"id":1,"name":"BUG","color":"red"},"keep"]}`)
	b := create(ticket.ID, "PAI-4", "backlog", root.ID, `{"classic":{"source_id":"x","assignee_id":7}}`)
	sub := create(task.ID, "PAI-5", "qa", a.ID, `{}`)

	bulk := func(body string, want int) bulkResult {
		t.Helper()
		status, raw := call(t, &p, http.MethodPost, "/api/nodes/bulk", body)
		return decode[bulkResult](t, status, raw, want)
	}
	get := func(id string) nodeJSON {
		t.Helper()
		status, raw := call(t, &p, http.MethodGet, "/api/nodes/"+id, "")
		return decode[nodeJSON](t, status, raw, http.StatusOK)
	}
	fields := func(n nodeJSON) map[string]any {
		var out map[string]any
		_ = json.Unmarshal(n.Fields, &out)
		return out
	}
	undo := func(eventID int64) (int, string) {
		t.Helper()
		status, raw := callAs(t, events.New(appPool, events.WithUndoHandlers(UndoHandlers())), &p, http.MethodPost, "/api/events/"+strconv.FormatInt(eventID, 10)+"/undo", "")
		return status, string(raw)
	}

	for _, body := range []string{`{"ids":[],"state":"done"}`, `{"ids":["` + a.ID + `"]}`, `{"ids":["nope"],"state":"done"}`,
		`{"ids":["` + a.ID + `"],"assignee":"someone"}`, `{"ids":["` + a.ID + `"],"tags_add":[""]}`, `{"ids":["` + a.ID + `"],"state":"done","extra":1}`} {
		if status, raw := call(t, &p, http.MethodPost, "/api/nodes/bulk", body); status != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, status, raw)
		}
	}

	// Archive two tickets; an unknown id is reported, not fatal; one event per ticket plus the batch.
	missing := "00000000-0000-4000-8000-000000000000"
	eventsBefore := len(tenantEvents(t, p.TenantID))
	archived := bulk(`{"ids":["`+a.ID+`","`+b.ID+`","`+missing+`"],"state":"archived"}`, http.StatusOK)
	if len(archived.Items) != 2 || archived.EventID == nil || len(archived.Skipped) != 1 || archived.Skipped[0].Reason != "not found" {
		t.Fatalf("archive: %#v", archived)
	}
	evs := tenantEvents(t, p.TenantID)[eventsBefore:]
	if len(evs) != 3 || evs[0].Type != evNodeUpdated || evs[1].Type != evNodeUpdated || evs[2].Type != evNodeBulkChanged || evs[2].NodeID != nil {
		t.Fatalf("archive events: %#v", evs)
	}
	if again := bulk(`{"ids":["`+a.ID+`"],"state":"archived"}`, http.StatusOK); len(again.Unchanged) != 1 || again.EventID != nil {
		t.Fatalf("no-op batch: %#v", again)
	}
	if status, raw := undo(*archived.EventID); status != http.StatusCreated {
		t.Fatalf("undo archive: %d %s", status, raw)
	}
	if get(a.ID).State != "new" || get(b.ID).State != "backlog" {
		t.Fatal("undo did not restore the states")
	}
	if status, _ := undo(*archived.EventID); status != http.StatusConflict {
		t.Fatalf("second undo: %d", status)
	}

	// Fields: explicit nulls clear (also over a classic assignee); tags merge case-insensitively.
	changed := bulk(`{"ids":["`+a.ID+`","`+b.ID+`"],"priority":null,"assignee":null,"tags_add":["ops",{"name":"UI","color":"blue"},"Keep"],"tags_remove":["bug"]}`, http.StatusOK)
	if len(changed.Items) != 2 {
		t.Fatalf("fields: %#v", changed)
	}
	fa, fb := fields(get(a.ID)), fields(get(b.ID))
	if fa["priority"] != nil || fa["assignee"] != nil || fb["assignee"] != nil {
		t.Fatalf("cleared fields: %#v %#v", fa, fb)
	}
	if _, ok := fb["assignee"]; !ok {
		t.Fatal("the classic assignee needs an explicit native null")
	}
	tags, _ := json.Marshal(fa["tags"])
	if string(tags) != `["keep",{"name":"ops"},{"color":"blue","name":"UI"}]` {
		t.Fatalf("tags: %s", tags)
	}
	status, raw := call(t, &p, http.MethodGet, "/api/nodes?within="+root.ID+"&assignee=none&kind=work", "")
	if page := decode[nodePage](t, status, raw, http.StatusOK); len(page.Items) != 2 {
		t.Fatalf("unassigned after bulk: %d", len(page.Items))
	}
	assigned := bulk(`{"ids":["`+b.ID+`"],"assignee":"`+p.ID+`","priority":"low"}`, http.StatusOK)
	if fb := fields(assigned.Items[0]); fb["assignee"] != p.ID || fb["priority"] != "low" {
		t.Fatalf("assign: %#v", fb)
	}

	// Parents: kind rules, cycles and other projects are skipped with reasons.
	status, raw = call(t, &p, http.MethodPatch, "/api/kinds/"+epic.ID, `{"allowed_child_kinds":["work"]}`)
	decode[kindJSON](t, status, raw, http.StatusOK)
	moved := bulk(`{"ids":["`+b.ID+`","`+sub.ID+`","`+e2.ID+`"],"parent_id":"`+e2.ID+`"}`, http.StatusOK)
	if len(moved.Items) != 1 || moved.Items[0].ID != b.ID || *moved.Items[0].ParentID != e2.ID || len(moved.Skipped) != 2 {
		t.Fatalf("move: %#v", moved)
	}
	reasons := map[string]string{}
	for _, s := range moved.Skipped {
		reasons[s.Key] = s.Reason
	}
	if reasons["PAI-5"] != "a task cannot sit under an epic" || reasons["PAI-2"] != "cannot move under itself" {
		t.Fatalf("move reasons: %#v", reasons)
	}
	if far := bulk(`{"ids":["`+a.ID+`"],"parent_id":"`+foreignEpic.ID+`"}`, http.StatusOK); len(far.Skipped) != 1 || far.Skipped[0].Reason != "belongs to another project" {
		t.Fatalf("foreign move: %#v", far)
	}
	evs = tenantEvents(t, p.TenantID)
	if last := evs[len(evs)-2]; last.Type != evNodeMoved || last.NodeID == nil || *last.NodeID != b.ID {
		t.Fatalf("move event: %#v", last)
	}

	// A batch that moves and edits undoes to the old parent; a later edit makes it stale.
	both := bulk(`{"ids":["`+a.ID+`"],"parent_id":"`+e2.ID+`","state":"qa"}`, http.StatusOK)
	if status, raw := undo(*both.EventID); status != http.StatusCreated {
		t.Fatalf("undo move: %d %s", status, raw)
	}
	if back := get(a.ID); *back.ParentID != e1.ID || back.State != "new" {
		t.Fatalf("undo move restored %#v", back)
	}
	stale := bulk(`{"ids":["`+a.ID+`","`+b.ID+`"],"state":"done"}`, http.StatusOK)
	status, raw = call(t, &p, http.MethodPatch, "/api/nodes/"+b.ID, `{"title":"Edited meanwhile"}`)
	decode[nodeJSON](t, status, raw, http.StatusOK)
	if status, body := undo(*stale.EventID); status != http.StatusConflict {
		t.Fatalf("stale undo: %d %s", status, body)
	}
	if get(a.ID).State != "done" {
		t.Fatal("a refused undo must change nothing")
	}
	if !strings.Contains(string(mustJSON(t, stale)), `"event_id"`) {
		t.Fatal("event id missing")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
