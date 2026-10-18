// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestCanonicalRouteFields(t *testing.T) {
	person := tenant.Principal{ID: "11111111-1111-4111-8111-111111111111", Kind: tenant.Person}
	kept := []byte(`{"priority":"low","route_role":"build","route_role_source":"person","route_role_by":"11111111-1111-4111-8111-111111111111","route_role_at":"2026-09-29T00:00:00Z"}`)
	out, err := canonicalRouteFields(person, "work", kept, kept)
	if err != nil || string(out) != string(kept) {
		t.Fatalf("unchanged: %s %v", out, err)
	}
	trimmed, err := canonicalRouteFields(person, "work", []byte(`{"route_role":" build ","area":" backend "}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	fields := routeFieldMap(t, trimmed)
	if fields["route_role"] != "build" || fields["area"] != "backend" || fields["route_role_source"] != "person" || fields["route_role_by"] != person.ID || fields["area_by"] != person.ID {
		t.Fatal(fields)
	}
	if fields["route_role_at"] == "" || fields["route_role_at"] != fields["area_at"] {
		t.Fatal(fields)
	}
	if _, err := time.Parse(time.RFC3339Nano, fields["route_role_at"].(string)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"route_role":"gruntwork"}`, `{"route_role":""}`, `{"area":"bad area"}`, `{"route_role":1}`} {
		if _, err := canonicalRouteFields(person, "work", []byte(raw), nil); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := canonicalRouteFields(person, "work", []byte(`{"route_role":"scout","route_role_source":"agent"}`), nil); err == nil || !strings.Contains(err.Error(), "route_role_source") {
		t.Fatal(err)
	}
	cleared, err := canonicalRouteFields(person, "work", []byte(`{"priority":"low","route_role":null,"route_role_source":"person"}`), kept)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := routeFieldMap(t, cleared)["route_role"]; ok {
		t.Fatalf("clear left role: %s", cleared)
	}
	project, err := canonicalRouteFields(person, "project", []byte(`{"area":"backend"}`), nil)
	if err != nil || string(project) != `{"area":"backend"}` {
		t.Fatalf("project fields rewritten: %s %v", project, err)
	}

	before := []byte(`{"priority":"low","route_role":"build","route_role_source":"person","route_role_by":"` + person.ID + `","route_role_at":"2026-09-29T00:00:00Z","area":"backend","area_source":"person","area_by":"` + person.ID + `","area_at":"2026-09-29T00:00:00Z"}`)
	forged, err := canonicalRouteFields(person, "work", []byte(`{"priority":"low","route_role":"build","route_role_source":"agent","route_role_by":"spoof","area":" backend "}`), before)
	if err != nil {
		t.Fatal(err)
	}
	fields = routeFieldMap(t, forged)
	if fields["route_role_source"] != "person" || fields["route_role_by"] != person.ID || fields["route_role_at"] != "2026-09-29T00:00:00Z" || fields["area_source"] != "person" || fields["area_by"] != person.ID || fields["area_at"] != "2026-09-29T00:00:00Z" || fields["area"] != "backend" {
		t.Fatalf("unchanged value restamped: %#v", fields)
	}
	agent := tenant.Principal{ID: "22222222-2222-4222-8222-222222222222", Kind: tenant.Agent}
	changed, err := canonicalRouteFields(agent, "work", []byte(`{"priority":"low","route_role":"mechanical","area":"backend"}`), before)
	if err != nil {
		t.Fatal(err)
	}
	fields = routeFieldMap(t, changed)
	if fields["route_role"] != "mechanical" || fields["route_role_source"] != "agent" || fields["route_role_by"] != agent.ID || fields["route_role_at"] == "2026-09-29T00:00:00Z" {
		t.Fatalf("role change did not restamp: %#v", fields)
	}
	if fields["area_source"] != "person" || fields["area_by"] != person.ID || fields["area_at"] != "2026-09-29T00:00:00Z" {
		t.Fatalf("unchanged area restamped: %#v", fields)
	}
}

func TestRouteRoleAndAreaProvenance(t *testing.T) {
	p := newPrincipal(t, "route-fields")
	agent := routeAgent(t, p.TenantID)
	ticketKind := kindBySlug(t, p, "work")
	taskKind := kindBySlug(t, p, "work")
	if !strings.Contains(string(ticketKind.FieldSchema), `"route_role"`) || !strings.Contains(string(taskKind.FieldSchema), `"area"`) {
		t.Fatalf("seed schema ticket %s task %s", ticketKind.FieldSchema, taskKind.FieldSchema)
	}
	n := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Route","fields":{"priority":"high","route_role":"build","area":"backend","route_role_by":"spoof","route_role_source":"person"}}`, ticketKind.ID))
	fields := routeFieldMap(t, n.Fields)
	if fields["route_role"] != "build" || fields["area"] != "backend" || fields["priority"] != "high" {
		t.Fatal(fields)
	}
	if fields["route_role_source"] != "person" || fields["area_source"] != "person" || fields["route_role_by"] != p.ID || fields["area_by"] != p.ID {
		t.Fatalf("provenance: %#v", fields)
	}
	if strings.Contains(string(n.Fields), "gpt-") || fields["model"] != nil {
		t.Fatalf("stored a model: %s", n.Fields)
	}
	roleAt := fields["route_role_at"].(string)
	areaAt := fields["area_at"].(string)
	if _, err := time.Parse(time.RFC3339Nano, roleAt); err != nil || roleAt != areaAt {
		t.Fatal(fields)
	}

	code, raw := call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"fields":{"route_role":"gruntwork","priority":"high"}}`)
	if code != 422 || !strings.Contains(string(raw), "route_role") {
		t.Fatalf("invalid role: %d %s", code, raw)
	}
	code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"fields":{"route_role":"scout","route_role_source":"agent","priority":"high"}}`)
	if code != 400 || !strings.Contains(string(raw), "route_role_source") {
		t.Fatalf("spoofed source: %d %s", code, raw)
	}
	code, raw = call(t, &p, "GET", "/api/nodes/"+n.ID, "")
	if got := decode[nodeJSON](t, code, raw, 200); string(got.Fields) != string(n.Fields) {
		t.Fatalf("rejected patch changed fields: %s", got.Fields)
	}

	code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"fields":{"priority":"high","route_role":"build","area":"backend","route_role_source":"agent","route_role_by":"spoof"}}`)
	repeated := decode[nodeJSON](t, code, raw, 200)
	fields = routeFieldMap(t, repeated.Fields)
	if fields["route_role_source"] != "person" || fields["area_source"] != "person" || fields["route_role_by"] != p.ID || fields["area_by"] != p.ID || fields["route_role_at"] != roleAt || fields["area_at"] != areaAt {
		t.Fatalf("repeat without provenance restamped: %#v", fields)
	}

	var same map[string]any
	if err := json.Unmarshal(n.Fields, &same); err != nil {
		t.Fatal(err)
	}
	same["priority"] = "low"
	body, _ := json.Marshal(map[string]any{"fields": same})
	code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, string(body))
	updated := decode[nodeJSON](t, code, raw, 200)
	fields = routeFieldMap(t, updated.Fields)
	if fields["priority"] != "low" || fields["route_role_at"] != roleAt || fields["area_at"] != areaAt || fields["route_role_by"] != p.ID {
		t.Fatalf("unchanged provenance moved: %#v", fields)
	}

	same = routeFieldMap(t, updated.Fields)
	same["route_role"] = "build-hard"
	same["route_role_by"] = "22222222-2222-4222-8222-222222222222"
	same["route_role_source"] = "person"
	body, _ = json.Marshal(map[string]any{"fields": same})
	code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, string(body))
	updated = decode[nodeJSON](t, code, raw, 200)
	fields = routeFieldMap(t, updated.Fields)
	if fields["route_role"] != "build-hard" || fields["route_role_by"] != p.ID || fields["area"] != "backend" || fields["area_at"] != areaAt || fields["area_by"] != p.ID {
		t.Fatalf("role rewrite: %#v", fields)
	}
	if fields["route_role_at"] == roleAt {
		t.Fatal("role timestamp was not refreshed")
	}

	code, raw = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"fields":{"route_role":"mechanical","area":"docs","priority":"low"}}`)
	updated = decode[nodeJSON](t, code, raw, 200)
	fields = routeFieldMap(t, updated.Fields)
	if fields["route_role_source"] != "agent" || fields["area_source"] != "agent" || fields["route_role_by"] != agent.ID || fields["area_by"] != agent.ID {
		t.Fatalf("agent provenance: %#v", fields)
	}

	code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"title":"Renamed"}`)
	if renamed := decode[nodeJSON](t, code, raw, 200); renamed.Title != "Renamed" || string(renamed.Fields) != string(updated.Fields) {
		t.Fatalf("title patch changed fields: %s", renamed.Fields)
	}
	code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"fields":{"priority":"low","route_role":null,"area":null}}`)
	cleared := decode[nodeJSON](t, code, raw, 200)
	fields = routeFieldMap(t, cleared.Fields)
	if _, ok := fields["route_role"]; ok {
		t.Fatal(fields)
	}
	if _, ok := fields["area"]; ok || fields["route_role_by"] != nil || fields["area_at"] != nil {
		t.Fatal(fields)
	}
	if fields["priority"] != "low" {
		t.Fatal(fields)
	}

	task := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Task route","fields":{"route_role":"scout","area":"design"}}`, taskKind.ID))
	fields = routeFieldMap(t, task.Fields)
	if fields["route_role_source"] != "person" || fields["area_by"] != p.ID {
		t.Fatal(fields)
	}
	projectKind := kindBySlug(t, p, "project")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project","fields":{"area":"backend"}}`, projectKind.ID))
	fields = routeFieldMap(t, project.Fields)
	if fields["area"] != "backend" || fields["area_source"] != nil {
		t.Fatalf("project area rewritten: %#v", fields)
	}
}

func routeAgent(t *testing.T, tenantID string) tenant.Principal {
	t.Helper()
	p := tenant.Principal{TenantID: tenantID, Kind: tenant.Agent, Name: "route-agent", Roles: []string{"admin"}}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'agent',$2,$3) RETURNING id::text`, tenantID, p.Name, p.Roles).Scan(&p.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	// BindLegacy only attaches people. The agent still needs a workspace role
	// or project RLS hides the ticket.
	dbtest.BindRole(t, testDB, tenantID, p.ID, "admin")
	return p
}

func routeFieldMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}
