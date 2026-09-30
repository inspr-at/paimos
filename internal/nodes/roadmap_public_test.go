// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

func TestCanonicalRoadmapPublication(t *testing.T) {
	person := tenant.Principal{ID: "11111111-1111-1111-1111-111111111111", TenantID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Kind: tenant.Person}
	agent := tenant.Principal{ID: "22222222-2222-2222-2222-222222222222", TenantID: person.TenantID, Kind: tenant.Agent}
	stored := []byte(`{"priority":"low","roadmap_public":true,"roadmap_public_source":"person","roadmap_public_by":"11111111-1111-1111-1111-111111111111","roadmap_public_at":"2026-09-30T00:00:00Z"}`)

	same, err := canonicalRoadmapPublication(agent, "ticket", []byte(`{"priority":"high","roadmap_public":true,"roadmap_public_source":"person","roadmap_public_by":"11111111-1111-1111-1111-111111111111","roadmap_public_at":"2026-09-30T00:00:00Z"}`), stored)
	if err != nil || string(same) != `{"priority":"high","roadmap_public":true,"roadmap_public_source":"person","roadmap_public_by":"11111111-1111-1111-1111-111111111111","roadmap_public_at":"2026-09-30T00:00:00Z"}` {
		t.Fatalf("unchanged true: %v %s", err, same)
	}
	kept, err := canonicalRoadmapPublication(agent, "ticket", []byte(`{"priority":"high","roadmap_public":true,"roadmap_public_source":"agent","roadmap_public_by":"spoof"}`), stored)
	if err != nil {
		t.Fatal(err)
	}
	keptFields := routeFieldMap(t, kept)
	if keptFields["priority"] != "high" || keptFields["roadmap_public_source"] != "person" || keptFields["roadmap_public_by"] != person.ID || keptFields["roadmap_public_at"] != "2026-09-30T00:00:00Z" {
		t.Fatalf("forged resend: %#v", keptFields)
	}
	if _, err := canonicalRoadmapPublication(agent, "ticket", []byte(`{"roadmap_public":true}`), nil); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("agent publish: %v", err)
	}
	if _, err := canonicalRoadmapPublication(agent, "ticket", []byte(`{"priority":"low"}`), stored); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("agent clear: %v", err)
	}
	absent, err := canonicalRoadmapPublication(agent, "ticket", []byte(`{"priority":"low","roadmap_public":false}`), nil)
	if err != nil || string(absent) != `{"priority":"low","roadmap_public":false}` {
		t.Fatalf("agent false: %v %s", err, absent)
	}
	stripped, err := canonicalRoadmapPublication(agent, "ticket", []byte(`{"roadmap_public":false,"roadmap_public_source":"person","roadmap_public_by":"spoof"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	strippedFields := routeFieldMap(t, stripped)
	if strippedFields["roadmap_public"] != false || strippedFields["roadmap_public_source"] != nil || strippedFields["roadmap_public_by"] != nil {
		t.Fatalf("forged false: %#v", strippedFields)
	}
	if _, err := canonicalRoadmapPublication(person, "ticket", []byte(`{"roadmap_public":"yes"}`), nil); err == nil || !strings.Contains(err.Error(), "roadmap_public must be a boolean") {
		t.Fatalf("string: %v", err)
	}
	if _, err := canonicalRoadmapPublication(person, "ticket", []byte(`{"roadmap_public":null}`), nil); err == nil || !strings.Contains(err.Error(), "roadmap_public must be a boolean") {
		t.Fatalf("null: %v", err)
	}
	if _, err := canonicalRoadmapPublication(person, "ticket", []byte(`{"roadmap_public":true,"roadmap_public_source":"agent"}`), nil); err == nil || !strings.Contains(err.Error(), "roadmap_public_source") {
		t.Fatalf("forged source: %v", err)
	}
	stamped, err := canonicalRoadmapPublication(person, "ticket", []byte(`{"priority":"low","roadmap_public":true,"roadmap_public_by":"spoof"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	stampedFields := routeFieldMap(t, stamped)
	if stampedFields["roadmap_public"] != true || stampedFields["roadmap_public_source"] != "person" || stampedFields["roadmap_public_by"] != person.ID || stampedFields["roadmap_public_at"] == "" || stampedFields["roadmap_public_at"] == "spoof" {
		t.Fatalf("stamp: %#v", stampedFields)
	}
	project, err := canonicalRoadmapPublication(person, "project", []byte(`{"roadmap_public":true}`), nil)
	if err != nil || string(project) != `{"roadmap_public":true}` {
		t.Fatalf("project: %v %s", err, project)
	}
	incomplete := []byte(`{"roadmap_public":true,"priority":"low"}`)
	agentKept, err := canonicalRoadmapPublication(agent, "ticket", []byte(`{"roadmap_public":true,"roadmap_public_source":"person","roadmap_public_by":"spoof","priority":"high"}`), incomplete)
	if err != nil {
		t.Fatal(err)
	}
	agentFields := routeFieldMap(t, agentKept)
	if agentFields["roadmap_public"] != true || agentFields["roadmap_public_source"] != nil || agentFields["priority"] != "high" {
		t.Fatalf("incomplete agent: %#v", agentFields)
	}
	personKept, err := canonicalRoadmapPublication(person, "ticket", []byte(`{"roadmap_public":true,"priority":"high"}`), incomplete)
	if err != nil {
		t.Fatal(err)
	}
	personFields := routeFieldMap(t, personKept)
	if personFields["roadmap_public_source"] != "person" || personFields["roadmap_public_by"] != person.ID || personFields["roadmap_public_at"] == "" {
		t.Fatalf("incomplete person: %#v", personFields)
	}
}

func TestRoadmapPublicationIsPersonOnly(t *testing.T) {
	p := newPrincipal(t, "roadmap-pub")
	ticketKind := kindBySlug(t, p, "ticket")
	taskKind := kindBySlug(t, p, "task")
	projectKind := kindBySlug(t, p, "project")
	agent := routeAgent(t, p.TenantID)

	created := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Public roadmap","fields":{"priority":"low","roadmap_public":true,"roadmap_public_source":"person","roadmap_public_by":"00000000-0000-0000-0000-000000000000"}}`, ticketKind.ID))
	fields := routeFieldMap(t, created.Fields)
	if fields["roadmap_public"] != true || fields["roadmap_public_source"] != "person" || fields["roadmap_public_by"] != p.ID || fields["roadmap_public_at"] == "" {
		t.Fatalf("create stamp: %#v", fields)
	}
	storedAt, _ := fields["roadmap_public_at"].(string)

	if code, raw := call(t, &agent, http.MethodPatch, "/api/nodes/"+created.ID, `{"fields":{"priority":"low"}}`); code != http.StatusForbidden || !strings.Contains(string(raw), "permission denied") {
		t.Fatalf("agent omit: %d %s", code, raw)
	}
	if code, raw := call(t, &agent, http.MethodPatch, "/api/nodes/"+created.ID, `{"fields":{"roadmap_public":false}}`); code != http.StatusForbidden || !strings.Contains(string(raw), "permission denied") {
		t.Fatalf("agent false: %d %s", code, raw)
	}
	if code, raw := call(t, &agent, http.MethodPatch, "/api/nodes/"+created.ID, `{"fields":{"roadmap_public":true,"roadmap_public_source":"agent","roadmap_public_by":"`+agent.ID+`","priority":"high"}}`); code != http.StatusOK {
		t.Fatalf("agent resend: %d %s", code, raw)
	} else {
		updated := decode[nodeJSON](t, code, raw, http.StatusOK)
		got := routeFieldMap(t, updated.Fields)
		if got["priority"] != "high" || got["roadmap_public_by"] != p.ID || got["roadmap_public_source"] != "person" || got["roadmap_public_at"] != storedAt {
			t.Fatalf("resend provenance: %#v", got)
		}
	}
	code, raw := call(t, &p, http.MethodGet, "/api/nodes/"+created.ID, "")
	if got := routeFieldMap(t, decode[nodeJSON](t, code, raw, http.StatusOK).Fields); got["roadmap_public_by"] != p.ID || got["roadmap_public_at"] != storedAt {
		t.Fatalf("stored after agent: %#v", got)
	}

	plain := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Ordinary ticket","fields":{"priority":"low"}}`, ticketKind.ID))
	code, raw = call(t, &agent, http.MethodPatch, "/api/nodes/"+plain.ID, `{"fields":{"priority":"high","roadmap_public":false}}`)
	ordinary := decode[nodeJSON](t, code, raw, http.StatusOK)
	got := routeFieldMap(t, ordinary.Fields)
	if got["priority"] != "high" || got["roadmap_public"] != false || got["roadmap_public_source"] != nil {
		t.Fatalf("agent false on absent: %#v", got)
	}
	if code, raw := call(t, &agent, http.MethodPatch, "/api/nodes/"+plain.ID, `{"fields":{"roadmap_public":true}}`); code != http.StatusForbidden {
		t.Fatalf("agent publish: %d %s", code, raw)
	}
	code, raw = call(t, &p, http.MethodGet, "/api/nodes/"+plain.ID, "")
	if got := routeFieldMap(t, decode[nodeJSON](t, code, raw, http.StatusOK).Fields); got["roadmap_public"] != false {
		t.Fatalf("agent publish wrote: %#v", got)
	}

	cleared := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Clear me","fields":{"roadmap_public":true,"priority":"low"}}`, ticketKind.ID))
	code, raw = call(t, &p, http.MethodPatch, "/api/nodes/"+cleared.ID, `{"fields":{"priority":"medium"}}`)
	clearedFields := routeFieldMap(t, decode[nodeJSON](t, code, raw, http.StatusOK).Fields)
	if _, ok := clearedFields["roadmap_public"]; ok || clearedFields["roadmap_public_source"] != nil || clearedFields["priority"] != "medium" {
		t.Fatalf("person clear: %#v", clearedFields)
	}

	if code, raw := call(t, &p, http.MethodPost, "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Bad flag","fields":{"roadmap_public":"yes"}}`, ticketKind.ID)); code != http.StatusUnprocessableEntity {
		t.Fatalf("non-bool: %d %s", code, raw)
	}
	if code, raw := call(t, &p, http.MethodPost, "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Forged source","fields":{"roadmap_public":true,"roadmap_public_source":"agent"}}`, ticketKind.ID)); code != http.StatusBadRequest || !strings.Contains(string(raw), "roadmap_public_source") {
		t.Fatalf("forged source: %d %s", code, raw)
	}

	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project","fields":{"roadmap_public":true,"roadmap_public_source":"agent"}}`, projectKind.ID))
	projectFields := routeFieldMap(t, project.Fields)
	if projectFields["roadmap_public"] != true || projectFields["roadmap_public_source"] != "agent" || projectFields["roadmap_public_by"] != nil {
		t.Fatalf("project rewritten: %#v", projectFields)
	}
	task := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Task","fields":{"roadmap_public":true}}`, taskKind.ID))
	taskFields := routeFieldMap(t, task.Fields)
	if taskFields["roadmap_public"] != true || taskFields["roadmap_public_source"] != nil {
		t.Fatalf("task rewritten: %#v", taskFields)
	}
}

func TestRoadmapPublicationKeepsNumbers(t *testing.T) {
	raw := json.RawMessage(`{"estimate_hours":1.5,"roadmap_public":false}`)
	out, err := canonicalRoadmapPublication(tenant.Principal{Kind: tenant.Agent}, "ticket", raw, nil)
	if err != nil || string(out) != string(raw) {
		t.Fatalf("number preserved: %v %s", err, out)
	}
}
