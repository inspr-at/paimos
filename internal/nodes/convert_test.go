// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestConvertIssueKindMatrix(t *testing.T) {
	p := newPrincipal(t, "kind-convert")
	customKind(t, p, "initiative", "epic")
	customKind(t, p, "chore", "task")
	project := kindBySlug(t, p, "project")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Convert project","state":"active"}`)
	for _, pair := range [][2]string{
		{"work", "initiative"},
		{"initiative", "work"},
		{"chore", "work"},
		{"work", "chore"},
		{"chore", "initiative"},
		{"initiative", "chore"},
	} {
		t.Run(pair[0]+" to "+pair[1], func(t *testing.T) {
			from := kindBySlug(t, p, pair[0])
			fields := `{}`
			if pair[0] == "work" {
				fields = benefitFields
			}
			node := mustNode(t, p, `{"kind_id":"`+from.ID+`","parent_id":"`+root.ID+`","title":"Convertible","fields":`+fields+`}`)
			status, raw := call(t, &p, http.MethodPost, "/api/nodes/"+node.ID+"/convert", `{"to_kind":"`+pair[1]+`"}`)
			got := decode[nodeJSON](t, status, raw, http.StatusOK)
			want := kindBySlug(t, p, pair[1])
			if got.KindID != want.ID || got.ID != node.ID || got.Key != node.Key || got.Title != node.Title {
				t.Fatalf("converted %#v key %s", got, node.Key)
			}
			if pair[0] == "work" && !strings.Contains(string(got.Fields), "pill_en") {
				t.Fatalf("fields dropped: %s", got.Fields)
			}
		})
	}
}

func TestConvertRefusesChildrenParentFieldsAgentsAndOtherTenants(t *testing.T) {
	p := newPrincipal(t, "kind-convert-block")
	customKind(t, p, "initiative", "epic")
	customKind(t, p, "chore", "task")
	projectKind := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "work")
	epicKind := kindBySlug(t, p, "initiative")
	root := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"Block project","state":"active"}`)
	epic := mustNode(t, p, `{"kind_id":"`+epicKind.ID+`","parent_id":"`+root.ID+`","title":"Parent epic"}`)
	child := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","parent_id":"`+epic.ID+`","title":"Child ticket","fields":`+benefitFields+`}`)
	status, raw := call(t, &p, http.MethodPatch, "/api/kinds/"+ticketKind.ID, `{"allowed_child_kinds":["chore"]}`)
	if status != http.StatusOK {
		t.Fatalf("restrict ticket children: %d %s", status, raw)
	}
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+epic.ID+"/convert", `{"to_kind":"work"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), codeKindConversionBlocked) || !strings.Contains(string(raw), child.Key) {
		t.Fatalf("child mismatch: %d %s", status, raw)
	}
	var blocked struct {
		Children []kindOffender `json:"children"`
		Fields   []string       `json:"fields"`
	}
	if err := json.Unmarshal(raw, &blocked); err != nil {
		t.Fatal(err)
	}
	if len(blocked.Children) != 1 || blocked.Children[0].Key != child.Key || blocked.Children[0].Kind != "work" {
		t.Fatalf("children %#v", blocked.Children)
	}
	still, _ := getNode(t, p, epic.ID)
	if still.KindID != epicKind.ID {
		t.Fatal("blocked convert wrote the kind")
	}

	status, raw = call(t, &p, http.MethodPatch, "/api/kinds/"+projectKind.ID, `{"allowed_child_kinds":["work","chore"]}`)
	if status != http.StatusOK {
		t.Fatalf("restrict project children: %d %s", status, raw)
	}
	loose := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","parent_id":"`+root.ID+`","title":"Under project","fields":`+benefitFields+`}`)
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+loose.ID+"/convert", `{"to_kind":"initiative"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), codeKindConversionBlocked) || !strings.Contains(string(raw), "parent does not allow initiative") {
		t.Fatalf("parent mismatch: %d %s", status, raw)
	}

	status, raw = call(t, &p, http.MethodPatch, "/api/kinds/"+epicKind.ID, `{"field_schema":{"type":"object","additionalProperties":false,"required":["points"],"properties":{"points":{"type":"number"}}}}`)
	if status != http.StatusOK {
		t.Fatalf("epic schema: %d %s", status, raw)
	}
	needy := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","parent_id":"`+root.ID+`","title":"Needs points","fields":`+benefitFields+`}`)
	status, raw = call(t, &p, http.MethodPatch, "/api/kinds/"+projectKind.ID, `{"allowed_child_kinds":null}`)
	if status != http.StatusOK {
		t.Fatalf("restore project children: %d %s", status, raw)
	}
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+needy.ID+"/convert", `{"to_kind":"initiative"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), `"points"`) || strings.Contains(string(raw), "pill_en") {
		t.Fatalf("field mismatch: %d %s", status, raw)
	}
	kept, _ := getNode(t, p, needy.ID)
	if !strings.Contains(string(kept.Fields), "pill_en") || kept.KindID != ticketKind.ID {
		t.Fatalf("fields changed on refusal: %s", kept.Fields)
	}

	agent := estimateAgent(t, p)
	before := kindChangedCount(t, p.TenantID)
	status, raw = call(t, &agent, http.MethodPost, "/api/nodes/"+loose.ID+"/convert", `{"to_kind":"chore"}`)
	if status != http.StatusForbidden {
		t.Fatalf("agent: %d %s", status, raw)
	}
	if kindChangedCount(t, p.TenantID) != before {
		t.Fatal("agent wrote an event")
	}

	other := addPrincipal(t, "kind-convert-other")
	status, raw = call(t, &other, http.MethodPost, "/api/nodes/"+loose.ID+"/convert", `{"to_kind":"chore"}`)
	if status != http.StatusNotFound {
		t.Fatalf("other tenant: %d %s", status, raw)
	}
	if kindChangedCount(t, p.TenantID) != before {
		t.Fatal("other tenant wrote an event")
	}

	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+root.ID+"/convert", `{"to_kind":"work"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), codeKindChangeNotAllowed) {
		t.Fatalf("project convert: %d %s", status, raw)
	}
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+loose.ID+"/convert", `{"to_kind":"project"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), codeKindChangeNotAllowed) {
		t.Fatalf("to project: %d %s", status, raw)
	}
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+loose.ID+"/convert", `{"to_kind":"work"}`)
	if status != http.StatusOK {
		t.Fatalf("same kind: %d %s", status, raw)
	}
	if kindChangedCount(t, p.TenantID) != before {
		t.Fatal("same kind wrote an event")
	}
}

func TestConvertWritesOneEventAndUndo(t *testing.T) {
	p := newPrincipal(t, "kind-convert-event")
	customKind(t, p, "initiative", "epic")
	customKind(t, p, "chore", "task")
	project := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "work")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Event project","state":"active"}`)
	ticket := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","parent_id":"`+root.ID+`","title":"Audited","fields":`+benefitFields+`}`)
	other := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","parent_id":"`+root.ID+`","title":"Related","fields":`+benefitFields+`}`)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields = fields || '{"scratch":"keep"}'::jsonb WHERE id = $1::uuid`, ticket.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO node_relations (tenant_id, source_node_id, target_node_id, type) VALUES ($1::uuid, $2::uuid, $3::uuid, 'blocks')`, p.TenantID, ticket.ID, other.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	status, raw := call(t, &p, http.MethodPost, "/api/nodes/"+ticket.ID+"/convert", `{"to_kind":"initiative"}`)
	got := decode[nodeJSON](t, status, raw, http.StatusOK)
	if got.Key != ticket.Key || got.ID != ticket.ID || !strings.Contains(string(got.Fields), "pill_en") || !strings.Contains(string(got.Fields), `"scratch":"keep"`) && !strings.Contains(string(got.Fields), `"scratch": "keep"`) {
		t.Fatalf("kept fields: %s", got.Fields)
	}
	if relationsOf(t, p.TenantID, ticket.ID) != 1 {
		t.Fatal("relation was dropped")
	}
	meta, after := kindChangedEvent(t, p.TenantID)
	if meta["from"] != "work" || meta["to"] != "initiative" || meta["by"] != p.ID {
		t.Fatalf("metadata %#v", meta)
	}
	if !strings.Contains(after, "pill_en") || !strings.Contains(after, "scratch") {
		t.Fatalf("event dropped fields: %s", after)
	}
	if kindChangedCount(t, p.TenantID) != 1 {
		t.Fatalf("events %d", kindChangedCount(t, p.TenantID))
	}

	undoMod := events.New(appPool, events.WithUndoHandlers(UndoHandlers()))
	id := latestKindChange(t, p.TenantID)
	status, raw = callAs(t, undoMod, &p, http.MethodPost, "/api/events/"+strconv.FormatInt(id, 10)+"/undo", "")
	if status != http.StatusCreated {
		t.Fatalf("undo: %d %s", status, raw)
	}
	restored, _ := getNode(t, p, ticket.ID)
	if restored.KindID != ticketKind.ID || !strings.Contains(string(restored.Fields), "scratch") {
		t.Fatalf("undo restore: kind %s fields %s", restored.KindID, restored.Fields)
	}

	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+ticket.ID+"/convert", `{"to_kind":"initiative"}`)
	if status != http.StatusOK {
		t.Fatalf("convert again: %d %s", status, raw)
	}
	status, raw = call(t, &p, http.MethodPatch, "/api/nodes/"+ticket.ID, `{"title":"Edited after"}`)
	if status != http.StatusOK {
		t.Fatalf("title: %d %s", status, raw)
	}
	id = latestKindChange(t, p.TenantID)
	status, raw = callAs(t, undoMod, &p, http.MethodPost, "/api/events/"+strconv.FormatInt(id, 10)+"/undo", "")
	if status != http.StatusConflict {
		t.Fatalf("stale undo: %d %s", status, raw)
	}
	edited, _ := getNode(t, p, ticket.ID)
	if edited.Title != "Edited after" || edited.KindID == ticketKind.ID {
		t.Fatalf("stale undo changed the node: %#v", edited)
	}
}

func TestConvertDropsFieldsOutsideAClosedSchema(t *testing.T) {
	p := newPrincipal(t, "kind-convert-fields")
	customKind(t, p, "initiative", "epic")
	customKind(t, p, "chore", "task")
	project := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "work")
	epicKind := kindBySlug(t, p, "initiative")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Field project","state":"active"}`)
	status, raw := call(t, &p, http.MethodPatch, "/api/kinds/"+epicKind.ID, `{"field_schema":{"type":"object","additionalProperties":false,"properties":{"priority":{"type":"string"}}}}`)
	if status != http.StatusOK {
		t.Fatalf("epic schema: %d %s", status, raw)
	}
	node := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","parent_id":"`+root.ID+`","title":"Carries extra","fields":{"priority":"high","scratch":"keep-me"}}`)
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+node.ID+"/convert", `{"to_kind":"initiative"}`)
	got := decode[nodeJSON](t, status, raw, http.StatusOK)
	if strings.Contains(string(got.Fields), `"scratch"`) || !strings.Contains(string(got.Fields), "high") || got.KindID != epicKind.ID {
		t.Fatalf("live fields: %s", got.Fields)
	}
	var before, after string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT before::text, after::text FROM events WHERE type = 'node.kind_changed' AND undo_of IS NULL ORDER BY id DESC LIMIT 1`).Scan(&before, &after)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before, "keep-me") || strings.Contains(after, `"scratch"`) {
		t.Fatalf("event before %s after %s", before, after)
	}
	status, raw = call(t, &p, http.MethodPatch, "/api/nodes/"+node.ID, `{"fields":{"priority":"low"}}`)
	if status != http.StatusOK || !strings.Contains(string(raw), "low") {
		t.Fatalf("patch after convert: %d %s", status, raw)
	}

	kept := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","parent_id":"`+root.ID+`","title":"Undo me","fields":{"priority":"high","scratch":"keep-me"}}`)
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+kept.ID+"/convert", `{"to_kind":"initiative"}`)
	if status != http.StatusOK {
		t.Fatalf("convert for undo: %d %s", status, raw)
	}
	undoMod := events.New(appPool, events.WithUndoHandlers(UndoHandlers()))
	id := latestKindChange(t, p.TenantID)
	status, raw = callAs(t, undoMod, &p, http.MethodPost, "/api/events/"+strconv.FormatInt(id, 10)+"/undo", "")
	if status != http.StatusCreated {
		t.Fatalf("undo: %d %s", status, raw)
	}
	restored, _ := getNode(t, p, kept.ID)
	if restored.KindID != ticketKind.ID || !strings.Contains(string(restored.Fields), "keep-me") || !strings.Contains(string(restored.Fields), "high") {
		t.Fatalf("undo restore: kind %s fields %s", restored.KindID, restored.Fields)
	}
}

func TestConvertValidatesRootSchemaConstraints(t *testing.T) {
	p := newPrincipal(t, "kind-convert-root")
	customKind(t, p, "initiative", "epic")
	customKind(t, p, "chore", "task")
	project := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "work")
	epicKind := kindBySlug(t, p, "initiative")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Root project","state":"active"}`)
	status, raw := call(t, &p, http.MethodPatch, "/api/kinds/"+epicKind.ID, `{"field_schema":{"type":"object","minProperties":1,"properties":{"priority":{"type":"string"}}}}`)
	if status != http.StatusOK {
		t.Fatalf("min schema: %d %s", status, raw)
	}
	empty := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","parent_id":"`+root.ID+`","title":"Empty","fields":{}}`)
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+empty.ID+"/convert", `{"to_kind":"initiative"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), "too few properties") || !strings.Contains(string(raw), codeKindConversionBlocked) {
		t.Fatalf("minProperties: %d %s", status, raw)
	}
	still, _ := getNode(t, p, empty.ID)
	if still.KindID != ticketKind.ID {
		t.Fatal("refused convert wrote the kind")
	}

	status, raw = call(t, &p, http.MethodPatch, "/api/kinds/"+epicKind.ID, `{"field_schema":{"type":"object","const":{"priority":"high"},"properties":{"priority":{"type":"string"}}}}`)
	if status != http.StatusOK {
		t.Fatalf("const schema: %d %s", status, raw)
	}
	low := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","parent_id":"`+root.ID+`","title":"Low","fields":{"priority":"low"}}`)
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+low.ID+"/convert", `{"to_kind":"initiative"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), "const") {
		t.Fatalf("const: %d %s", status, raw)
	}
	kept, _ := getNode(t, p, low.ID)
	if kept.KindID != ticketKind.ID || !strings.Contains(string(kept.Fields), "low") {
		t.Fatalf("const refusal changed the node: %s", kept.Fields)
	}
}

func TestConvertCustomIssueFamily(t *testing.T) {
	p := newPrincipal(t, "kind-convert-family")
	project := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "work")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Family project","state":"active"}`)
	status, raw := call(t, &p, http.MethodPost, "/api/kinds", `{
		"slug":"story","label":"Story","short_prefix":"STY","icon":"book",
		"allowed_child_kinds":["initiative"],
		"field_schema":{"type":"object","issue_family":true}
	}`)
	story := decode[kindJSON](t, status, raw, http.StatusCreated)
	status, raw = call(t, &p, http.MethodPost, "/api/kinds", `{
		"slug":"initiative","label":"Initiative","short_prefix":"INI","icon":"flag",
		"allowed_child_kinds":[],
		"field_schema":{"type":"object","issue_family":true}
	}`)
	initiative := decode[kindJSON](t, status, raw, http.StatusCreated)
	status, raw = call(t, &p, http.MethodPost, "/api/kinds", `{
		"slug":"memo","label":"Memo","short_prefix":"MMO","icon":"ticket",
		"field_schema":{"type":"object","issue_family":false}
	}`)
	if status != http.StatusCreated {
		t.Fatalf("memo: %d %s", status, raw)
	}
	status, raw = call(t, &p, http.MethodPost, "/api/kinds", `{
		"slug":"chore","label":"Chore","short_prefix":"CHO","icon":"ticket",
		"field_schema":{"type":"object"}
	}`)
	chore := decode[kindJSON](t, status, raw, http.StatusCreated)

	node := mustNode(t, p, `{"kind_id":"`+story.ID+`","parent_id":"`+root.ID+`","title":"Custom"}`)
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+node.ID+"/convert", `{"to_kind":"initiative"}`)
	got := decode[nodeJSON](t, status, raw, http.StatusOK)
	if got.KindID != initiative.ID || got.Key != node.Key {
		t.Fatalf("custom convert %#v", got)
	}
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+node.ID+"/convert", `{"to_kind":"release"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), codeKindChangeNotAllowed) {
		t.Fatalf("release: %d %s", status, raw)
	}
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+node.ID+"/convert", `{"to_kind":"memo"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), codeKindChangeNotAllowed) {
		t.Fatalf("explicit false: %d %s", status, raw)
	}

	plain := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","parent_id":"`+root.ID+`","title":"Seeded"}`)
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+plain.ID+"/convert", `{"to_kind":"chore"}`)
	got = decode[nodeJSON](t, status, raw, http.StatusOK)
	if got.KindID != chore.ID {
		t.Fatalf("icon family %#v", got)
	}

	parent := mustNode(t, p, `{"kind_id":"`+story.ID+`","parent_id":"`+root.ID+`","title":"Parent story"}`)
	child := mustNode(t, p, `{"kind_id":"`+initiative.ID+`","parent_id":"`+parent.ID+`","title":"Child initiative"}`)
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+child.ID+"/convert", `{"to_kind":"story"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), codeKindConversionBlocked) || !strings.Contains(string(raw), "parent does not allow story") {
		t.Fatalf("parent rule: %d %s", status, raw)
	}
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+parent.ID+"/convert", `{"to_kind":"initiative"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), codeKindConversionBlocked) || !strings.Contains(string(raw), child.Key) {
		t.Fatalf("empty children: %d %s", status, raw)
	}
}

func getNode(t *testing.T, p tenant.Principal, id string) (nodeJSON, []byte) {
	t.Helper()
	status, raw := call(t, &p, http.MethodGet, "/api/nodes/"+id, "")
	return decode[nodeJSON](t, status, raw, http.StatusOK), raw
}

func kindChangedCount(t *testing.T, tenantID string) int {
	t.Helper()
	var n int
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type = 'node.kind_changed'`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func kindChangedEvent(t *testing.T, tenantID string) (map[string]string, string) {
	t.Helper()
	var meta, after string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT metadata::text, after::text FROM events WHERE type = 'node.kind_changed' AND undo_of IS NULL ORDER BY id DESC LIMIT 1`).Scan(&meta, &after)
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(meta), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded, after
}

func latestKindChange(t *testing.T, tenantID string) int64 {
	t.Helper()
	var id int64
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE type = 'node.kind_changed' AND undo_of IS NULL ORDER BY id DESC LIMIT 1`).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func relationsOf(t *testing.T, tenantID, nodeID string) int {
	t.Helper()
	var n int
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM node_relations WHERE source_node_id = $1::uuid OR target_node_id = $1::uuid`, nodeID).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
