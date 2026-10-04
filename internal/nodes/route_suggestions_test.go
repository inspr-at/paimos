// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestEstimateComplexityBucketsAndHints(t *testing.T) {
	for _, tc := range []struct {
		hours      float64
		role, want string
	}{
		{0.5, "build", "S"}, {2, "build", "S"}, {2.01, "build", "M"}, {8, "build", "M"}, {8.01, "build", "L"},
		{2, "build-hard", "M"}, {8, "build-hard", "L"}, {200, "build-hard", "L"}, {1, "scout", "S"},
	} {
		if got := estimateComplexity(tc.hours, tc.role); got != tc.want {
			t.Errorf("%g %s: %s want %s", tc.hours, tc.role, got, tc.want)
		}
	}
	for _, tc := range []struct {
		title, role, area string
		fields            map[string]any
	}{
		{"Secure RLS migration", "build-hard", "security", nil},
		{"UI and API integration", "", "full-stack", nil},
		{"README cleanup", "mechanical", "docs", nil},
		{"Investigate Docker deployment", "scout", "infra", nil},
		{"Untyped work", "", "frontend", map[string]any{"labels": []any{"frontend"}}},
		{"Typography", "", "design", nil},
	} {
		role, area := routeHints(tc.title, tc.fields)
		if role != tc.role || area != tc.area {
			t.Errorf("%s: %s/%s", tc.title, role, area)
		}
	}
}

func TestRouteClassificationConfirmsAgentAndClearsProvenance(t *testing.T) {
	agent := tenant.Principal{ID: "11111111-1111-4111-8111-111111111111", Kind: tenant.Agent}
	person := tenant.Principal{ID: "22222222-2222-4222-8222-222222222222", Kind: tenant.Person}
	raw, err := canonicalRouteFields(agent, "task", []byte(`{"complexity":"M","complexity_confirmed":true}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	fields := routeFieldMap(t, raw)
	if fields["complexity_confirmed"] != false || fields["complexity_source"] != "agent" {
		t.Fatal(fields)
	}
	confirmed, err := canonicalRouteFields(person, "task", []byte(`{"complexity":"M"}`), raw)
	if err != nil {
		t.Fatal(err)
	}
	fields = routeFieldMap(t, confirmed)
	if fields["complexity_source"] != "person" || fields["complexity_by"] != person.ID || fields["complexity_confirmed"] != true {
		t.Fatal(fields)
	}
	cleared, err := canonicalRouteFields(person, "task", []byte(`{"complexity":null}`), confirmed)
	if err != nil || len(routeFieldMap(t, cleared)) != 0 {
		t.Fatalf("clear left provenance: %s %v", cleared, err)
	}
	if _, err := canonicalRouteFields(person, "ticket", []byte(`{"complexity":"M","complexity_source":"suggested"}`), nil); err == nil {
		t.Fatal("client forged a server suggestion")
	}
}

func TestEstimatedTicketSuggestionsAndConfirmation(t *testing.T) {
	p := newPrincipal(t, "estimate-routes")
	agent := estimateAgent(t, p)
	kind := kindBySlug(t, p, "ticket")
	n := mustNode(t, agent, fmt.Sprintf(`{"kind_id":%q,"title":"RLS schema migration","fields":{"estimate_hours":2}}`, kind.ID))
	fields := routeFieldMap(t, n.Fields)
	for key, want := range map[string]string{"route_role": "build-hard", "area": "security", "complexity": "M"} {
		if fields[key] != want || fields[key+"_source"] != "suggested" || fields[key+"_by"] != agent.ID || fields[key+"_confirmed"] != false || fields[key+"_at"] == nil {
			t.Fatalf("suggestion %s: %#v", key, fields)
		}
	}
	// A copied/forged stamp cannot confirm a suggestion, even for a person.
	fields["route_role_confirmed"] = true
	fields["route_role_by"] = "forged"
	body, _ := json.Marshal(map[string]any{"fields": fields})
	code, raw := call(t, &p, "PATCH", "/api/nodes/"+n.ID, string(body))
	n = decode[nodeJSON](t, code, raw, 200)
	fields = routeFieldMap(t, n.Fields)
	if fields["route_role_confirmed"] != false || fields["route_role_by"] != agent.ID {
		t.Fatal(fields)
	}
	roleAt := fields["route_role_at"]
	// An agent cannot confirm by resubmitting the same values without stamps.
	for _, key := range []string{"route_role_source", "route_role_by", "route_role_at"} {
		delete(fields, key)
	}
	body, _ = json.Marshal(map[string]any{"fields": fields})
	code, raw = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, string(body))
	n = decode[nodeJSON](t, code, raw, 200)
	fields = routeFieldMap(t, n.Fields)
	if fields["route_role_source"] != "suggested" || fields["route_role_at"] != roleAt {
		t.Fatal(fields)
	}
	// A person explicitly confirms the identical role, area and complexity.
	for _, group := range routeGroups {
		delete(fields, group.source)
		delete(fields, group.by)
		delete(fields, group.at)
	}
	body, _ = json.Marshal(map[string]any{"fields": fields})
	code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, string(body))
	n = decode[nodeJSON](t, code, raw, 200)
	fields = routeFieldMap(t, n.Fields)
	for _, group := range routeGroups {
		if fields[group.source] != "person" || fields[group.by] != p.ID || fields[group.value+"_confirmed"] != true {
			t.Fatal(fields)
		}
	}
	// An estimate change preserves confirmed complexity and all route stamps.
	code, raw = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"estimate_hours":20}`)
	n = decode[nodeJSON](t, code, raw, 200)
	after := routeFieldMap(t, n.Fields)
	if after["complexity"] != "M" || after["complexity_at"] != fields["complexity_at"] {
		t.Fatal(after)
	}
}

func TestReleaseTextEditPreservesMissingRouteHints(t *testing.T) {
	p := newPrincipal(t, "release-text-routes")
	agent := estimateAgent(t, p)
	kind := kindBySlug(t, p, "ticket")
	n := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Docker deployment","body":"Keep this body","fields":{"estimate_hours":4,"priority":"high","hide_from_release_notes":true,"notes":{"nested":["keep",42]},"pill_en":"Old release text","pill_de":"Alter kurzer Text","benefit_en":"Old benefit.","benefit_de":"Alter Nutzen."}}`, kind.ID))
	// Seed an existing estimated ticket from before route suggestions existed.
	// Keep its real estimate provenance so a replacement document is not a new estimate.
	fields := estimateFieldsOf(t, n)
	for _, key := range []string{
		"route_role", "route_role_source", "route_role_by", "route_role_at", "route_role_confirmed",
		"area", "area_source", "area_by", "area_at", "area_confirmed",
		"complexity", "complexity_source", "complexity_by", "complexity_at", "complexity_confirmed",
	} {
		delete(fields, key)
	}
	seed, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, n.ID, seed)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	code, raw := call(t, &agent, "GET", "/api/nodes/"+n.ID, "")
	n = decode[nodeJSON](t, code, raw, 200)
	if got := estimateFieldsOf(t, n); !reflect.DeepEqual(got, fields) {
		t.Fatalf("legacy fixture changed: got %#v, want %#v", got, fields)
	}
	want := maps.Clone(fields)
	want["pill_en"], want["pill_de"] = "Clear release text", "Klarer kurzer Text"
	want["benefit_en"], want["benefit_de"] = "Release notes describe the change.", "Versionshinweise beschreiben die Änderung."
	fields = maps.Clone(want)
	// Equal numeric hours are unchanged even if their JSON spelling differs.
	fields["estimate_hours"] = json.Number("4.0")
	body, err := json.Marshal(map[string]any{"fields": fields})
	if err != nil {
		t.Fatal(err)
	}
	code, raw = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, string(body))
	updated := decode[nodeJSON](t, code, raw, 200)
	if got := estimateFieldsOf(t, updated); !reflect.DeepEqual(got, want) {
		t.Fatalf("release text edit changed unrelated fields: got %#v, want %#v", got, want)
	}
	code, raw = call(t, &agent, "GET", "/api/nodes/"+n.ID, "")
	stored := decode[nodeJSON](t, code, raw, 200)
	if got := estimateFieldsOf(t, stored); !reflect.DeepEqual(got, want) {
		t.Fatalf("stored fields differ after release text edit: got %#v, want %#v", got, want)
	}
	if stored.Title != n.Title || stored.Body != n.Body || stored.State != n.State || stored.Key != n.Key || stored.KindID != n.KindID || !reflect.DeepEqual(stored.ParentID, n.ParentID) {
		t.Fatal("release text edit changed unrelated node properties")
	}
	// A real estimate change still fills the previously missing hints.
	code, raw = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"estimate_hours":9}`)
	updated = decode[nodeJSON](t, code, raw, 200)
	got := estimateFieldsOf(t, updated)
	for key, value := range map[string]string{"route_role": "build", "area": "infra", "complexity": "L"} {
		if got[key] != value || got[key+"_source"] != "suggested" || got[key+"_by"] != agent.ID || got[key+"_confirmed"] != false {
			t.Fatalf("changed estimate did not suggest %s: %#v", key, got)
		}
	}
}

func TestEstimateRouteDefaultsAndDerivedComplexity(t *testing.T) {
	p := newPrincipal(t, "estimate-defaults")
	projectKind := kindBySlug(t, p, "project")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Product","fields":{"route_role":"build-hard","area":"frontend"}}`, projectKind.ID))
	epicKind := kindBySlug(t, p, "epic")
	epic := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Epic","parent_id":%q,"fields":{"area":"docs"}}`, epicKind.ID, project.ID))
	ticketKind := kindBySlug(t, p, "ticket")
	n := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"General work","parent_id":%q,"fields":{"estimate_hours":1}}`, ticketKind.ID, epic.ID))
	fields := routeFieldMap(t, n.Fields)
	if fields["route_role"] != "build-hard" || fields["area"] != "docs" || fields["complexity"] != "M" {
		t.Fatal(fields)
	}
	code, raw := call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"estimate_hours":5}`)
	n = decode[nodeJSON](t, code, raw, 200)
	fields = routeFieldMap(t, n.Fields)
	if fields["complexity"] != "L" || fields["complexity_source"] != "suggested" || fields["complexity_confirmed"] != false {
		t.Fatal(fields)
	}
	// Explicit caller classification wins over all suggestions and is stamped.
	n = mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"RLS schema migration","fields":{"estimate_hours":1,"route_role":"scout","area":"design","complexity":"S","complexity_by":"forged","complexity_confirmed":false}}`, ticketKind.ID))
	fields = routeFieldMap(t, n.Fields)
	if fields["route_role"] != "scout" || fields["area"] != "design" || fields["complexity"] != "S" || fields["complexity_by"] != p.ID || fields["complexity_confirmed"] != true {
		t.Fatal(fields)
	}
	// Missing hints use documented defaults. Unestimated tickets stay untouched.
	n = mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"General work","fields":{"estimate_hours":1}}`, ticketKind.ID))
	fields = routeFieldMap(t, n.Fields)
	if fields["route_role"] != "build" || fields["area"] != "backend" || fields["complexity"] != "S" {
		t.Fatal(fields)
	}
	n = mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"RLS migration","fields":{}}`, ticketKind.ID))
	if fields = routeFieldMap(t, n.Fields); fields["route_role"] != nil || fields["complexity"] != nil {
		t.Fatal(fields)
	}
	for _, raw := range []string{`{"complexity":"XL"}`, `{"complexity":true}`, `{"complexity":"S","complexity_source":"person"}`} {
		if _, err := canonicalRouteFields(tenant.Principal{ID: p.ID, Kind: tenant.Agent}, "ticket", []byte(raw), nil); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
