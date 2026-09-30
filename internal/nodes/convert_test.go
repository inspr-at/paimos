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
	project := kindBySlug(t, p, "project")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Convert project","state":"active"}`)
	for _, pair := range [][2]string{
		{"ticket", "epic"},
		{"epic", "ticket"},
		{"task", "ticket"},
		{"ticket", "task"},
		{"task", "epic"},
		{"epic", "task"},
	} {
		t.Run(pair[0]+" to "+pair[1], func(t *testing.T) {
			from := kindBySlug(t, p, pair[0])
			fields := `{}`
			if pair[0] == "ticket" {
				fields = benefitFields
			}
			node := mustNode(t, p, `{"kind_id":"`+from.ID+`","parent_id":"`+root.ID+`","title":"Convertible","fields":`+fields+`}`)
			status, raw := call(t, &p, http.MethodPost, "/api/nodes/"+node.ID+"/convert", `{"to_kind":"`+pair[1]+`"}`)
			got := decode[nodeJSON](t, status, raw, http.StatusOK)
			want := kindBySlug(t, p, pair[1])
			if got.KindID != want.ID || got.ID != node.ID || got.Key != node.Key || got.Title != node.Title {
				t.Fatalf("converted %#v key %s", got, node.Key)
			}
			if pair[0] == "ticket" && !strings.Contains(string(got.Fields), "pill_en") {
				t.Fatalf("fields dropped: %s", got.Fields)
			}
		})
	}
}

func TestConvertRefusesChildrenParentFieldsAgentsAndOtherTenants(t *testing.T) {
	p := newPrincipal(t, "kind-convert-block")
	projectKind := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "ticket")
	epicKind := kindBySlug(t, p, "epic")
	root := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"Block project","state":"active"}`)
	epic := mustNode(t, p, `{"kind_id":"`+epicKind.ID+`","parent_id":"`+root.ID+`","title":"Parent epic"}`)
	child := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","parent_id":"`+epic.ID+`","title":"Child ticket","fields":`+benefitFields+`}`)
	status, raw := call(t, &p, http.MethodPatch, "/api/kinds/"+ticketKind.ID, `{"allowed_child_kinds":["task"]}`)
	if status != http.StatusOK {
		t.Fatalf("restrict ticket children: %d %s", status, raw)
	}
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+epic.ID+"/convert", `{"to_kind":"ticket"}`)
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
	if len(blocked.Children) != 1 || blocked.Children[0].Key != child.Key || blocked.Children[0].Kind != "ticket" {
		t.Fatalf("children %#v", blocked.Children)
	}
	still, _ := getNode(t, p, epic.ID)
	if still.KindID != epicKind.ID {
		t.Fatal("blocked convert wrote the kind")
	}

	status, raw = call(t, &p, http.MethodPatch, "/api/kinds/"+projectKind.ID, `{"allowed_child_kinds":["ticket","task"]}`)
	if status != http.StatusOK {
		t.Fatalf("restrict project children: %d %s", status, raw)
	}
	loose := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","parent_id":"`+root.ID+`","title":"Under project","fields":`+benefitFields+`}`)
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+loose.ID+"/convert", `{"to_kind":"epic"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), codeKindConversionBlocked) || !strings.Contains(string(raw), "parent does not allow epic") {
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
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+needy.ID+"/convert", `{"to_kind":"epic"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), `"points"`) || strings.Contains(string(raw), "pill_en") {
		t.Fatalf("field mismatch: %d %s", status, raw)
	}
	kept, _ := getNode(t, p, needy.ID)
	if !strings.Contains(string(kept.Fields), "pill_en") || kept.KindID != ticketKind.ID {
		t.Fatalf("fields changed on refusal: %s", kept.Fields)
	}

	agent := estimateAgent(t, p)
	before := kindChangedCount(t, p.TenantID)
	status, raw = call(t, &agent, http.MethodPost, "/api/nodes/"+loose.ID+"/convert", `{"to_kind":"task"}`)
	if status != http.StatusForbidden {
		t.Fatalf("agent: %d %s", status, raw)
	}
	if kindChangedCount(t, p.TenantID) != before {
		t.Fatal("agent wrote an event")
	}

	other := addPrincipal(t, "kind-convert-other")
	status, raw = call(t, &other, http.MethodPost, "/api/nodes/"+loose.ID+"/convert", `{"to_kind":"task"}`)
	if status != http.StatusNotFound {
		t.Fatalf("other tenant: %d %s", status, raw)
	}
	if kindChangedCount(t, p.TenantID) != before {
		t.Fatal("other tenant wrote an event")
	}

	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+root.ID+"/convert", `{"to_kind":"ticket"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), codeKindChangeNotAllowed) {
		t.Fatalf("project convert: %d %s", status, raw)
	}
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+loose.ID+"/convert", `{"to_kind":"project"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), codeKindChangeNotAllowed) {
		t.Fatalf("to project: %d %s", status, raw)
	}
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+loose.ID+"/convert", `{"to_kind":"ticket"}`)
	if status != http.StatusOK {
		t.Fatalf("same kind: %d %s", status, raw)
	}
	if kindChangedCount(t, p.TenantID) != before {
		t.Fatal("same kind wrote an event")
	}
}

func TestConvertWritesOneEventAndUndo(t *testing.T) {
	p := newPrincipal(t, "kind-convert-event")
	project := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "ticket")
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
	status, raw := call(t, &p, http.MethodPost, "/api/nodes/"+ticket.ID+"/convert", `{"to_kind":"epic"}`)
	got := decode[nodeJSON](t, status, raw, http.StatusOK)
	if got.Key != ticket.Key || got.ID != ticket.ID || !strings.Contains(string(got.Fields), "pill_en") || !strings.Contains(string(got.Fields), `"scratch":"keep"`) && !strings.Contains(string(got.Fields), `"scratch": "keep"`) {
		t.Fatalf("kept fields: %s", got.Fields)
	}
	if relationsOf(t, p.TenantID, ticket.ID) != 1 {
		t.Fatal("relation was dropped")
	}
	meta, after := kindChangedEvent(t, p.TenantID)
	if meta["from"] != "ticket" || meta["to"] != "epic" || meta["by"] != p.ID {
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

	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+ticket.ID+"/convert", `{"to_kind":"epic"}`)
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
