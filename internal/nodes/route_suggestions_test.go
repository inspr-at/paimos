// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
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
		{"Secure RLS migration", "build-hard", "backend", nil},
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
	for key, want := range map[string]string{"route_role": "build-hard", "area": "backend", "complexity": "M"} {
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
