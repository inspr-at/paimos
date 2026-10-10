// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

func TestWorkName(t *testing.T) {
	v := workVocabulary{Leaf: workLevel{Name: "Step", Icon: "check"}, Levels: []workLevel{{Name: "Feature", Icon: "tree"}}}
	for _, tc := range []struct {
		leaf       bool
		depth      int
		name, icon string
	}{{true, 1, "Step", "check"}, {true, 12, "Step", "check"}, {false, 1, "Feature", "tree"}, {false, 2, "Story", "layers"}, {false, 8, "Level 8", "layers"}} {
		got := workName(v, tc.leaf, tc.depth)
		if got.Name != tc.name || got.Icon != tc.icon {
			t.Fatalf("%+v: %+v", tc, got)
		}
	}
	for _, v := range []workVocabulary{{Revision: -1}, {Leaf: workLevel{Name: strings.Repeat("ö", 61)}}, {Leaf: workLevel{Name: "Unsafe\nname"}}, {Leaf: workLevel{Icon: "<script>"}}, {Levels: make([]workLevel, 33)}} {
		if v.validate() == nil {
			t.Fatalf("accepted %+v", v)
		}
	}
}
func TestWorkVocabularyRoundTripAndShape(t *testing.T) {
	p := newPrincipal(t, "vocabulary")
	status, body := call(t, &p, "GET", "/api/settings/work-vocabulary", "")
	initial := decode[workVocabulary](t, status, body, 200)
	if initial.Revision != 0 || len(initial.Levels) != 0 {
		t.Fatalf("defaults %+v", initial)
	}
	in := `{"revision":0,"leaf":{"name":"Schritt mit ausführlicher Beschreibung","icon":"check"},"levels":[{"name":"Vorhaben","icon":"tree"}]}`
	status, body = call(t, &p, "PUT", "/api/settings/work-vocabulary", in)
	saved := decode[workVocabulary](t, status, body, 200)
	if saved.Revision != 1 || saved.Levels[0].Name != "Vorhaben" {
		t.Fatalf("saved %+v", saved)
	}
	status, body = call(t, &p, "PUT", "/api/settings/work-vocabulary", in)
	if status != 409 || !strings.Contains(string(body), "reload") {
		t.Fatalf("stale %d %s", status, body)
	}
	agent := tenant.Principal{ID: insertNamedAgent(t, p.TenantID, "vocabulary-agent"), TenantID: p.TenantID, Kind: tenant.Agent}
	if status, _ = call(t, &agent, "PUT", "/api/settings/work-vocabulary", in); status != 403 {
		t.Fatalf("agent write %d", status)
	}
	other := addPrincipal(t, "other-vocabulary")
	status, body = call(t, &other, "GET", "/api/settings/work-vocabulary", "")
	if decode[workVocabulary](t, status, body, 200).Revision != 0 {
		t.Fatal("tenant vocabulary leaked")
	}
	for _, bad := range []string{in + ` {}`, strings.Replace(in, `"revision":0`, `"revision":1,"unexpected":1`, 1), strings.Repeat("x", 8193), `{"levels":[]}`, `{"revision":1,"leaf":{"name":""},"levels":[]}`, `{"revision":1,"leaf":{"name":"","icon":""},"levels":[{}]}`} {
		if status, _ = call(t, &p, "PUT", "/api/settings/work-vocabulary", bad); status != 400 {
			t.Fatalf("invalid body %d", status)
		}
	}
	projectRoot := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Work project"}`, kindBySlug(t, p, "project").ID))
	root := workNodeTest(t, p, projectRoot.ID, "open")
	child := workNodeTest(t, p, root.ID, "open")
	before := currentWorkTest(t, p, root.ID)
	if before.IsLeaf || before.Depth != 1 || before.StatusDerived || before.LevelName != "Work item" {
		t.Fatalf("flag off %+v", before.WorkShape)
	}
	enableWorkStatusTest(t, p)
	parent := currentWorkTest(t, p, root.ID)
	leaf := currentWorkTest(t, p, child.ID)
	if parent.IsLeaf || parent.WorkChildrenCount != 1 || !parent.StatusDerived || parent.LevelName != "Vorhaben" || parent.LevelIcon != "tree" {
		t.Fatalf("parent %+v", parent.WorkShape)
	}
	if !leaf.IsLeaf || leaf.Depth != 2 || leaf.LevelName != saved.Leaf.Name {
		t.Fatalf("leaf %+v", leaf.WorkShape)
	}
	for _, tc := range []struct{ filter, id string }{{"shape=parent", root.ID}, {"shape=leaf&depth=2", child.ID}, {"kind=epic&shape=leaf", child.ID}, {"shape=!parent", child.ID},
		// AEON-974: the Type filter's level is the leaf, else the parent's depth.
		{"level=1", root.ID}, {"level=leaf", child.ID}, {"level=!leaf", root.ID}, {"level=leaf,2", child.ID}, {"level=!1&depth=2", child.ID}} {
		status, body = call(t, &p, "GET", "/api/nodes?"+tc.filter, "")
		page := decode[nodePage](t, status, body, 200)
		if len(page.Items) != 1 || page.Items[0].ID != tc.id || page.Items[0].WorkShape == nil {
			t.Fatalf("%s: %s", tc.filter, body)
		}
	}

	status, body = call(t, &p, "GET", "/api/nodes?within="+projectRoot.ID+"&facets=shape,depth,level", "")
	facets := decode[nodePage](t, status, body, 200).Facets
	if facets["shape"]["parent"] != 1 || facets["shape"]["leaf"] != 1 || facets["depth"]["2"] != 1 {
		t.Fatalf("shape facets %+v", facets)
	}
	if len(facets["level"]) != 2 || facets["level"]["1"] != 1 || facets["level"]["leaf"] != 1 {
		t.Fatalf("level facets %+v", facets["level"])
	}
	for _, bad := range []string{"level=parent", "level=0", "level=!", tooManyLevels()} {
		if status, body = call(t, &p, "GET", "/api/nodes?"+bad, ""); status != 400 {
			t.Fatalf("%s: %d %s", bad, status, body)
		}
	}
	status, body = call(t, &p, "GET", "/api/tickets/graph?project_id="+projectRoot.ID, "")
	graph := decode[TicketGraph](t, status, body, 200)
	if len(graph.Nodes) != 2 || len(graph.Links) != 1 {
		t.Fatalf("work graph %+v", graph)
	}
	for _, n := range graph.Nodes {
		if n.WorkShape == nil || n.Type != "work" {
			t.Fatalf("graph shape %+v", n)
		}
	}
	status, body = call(t, &p, "PATCH", "/api/nodes/"+child.ID, `{"title":"Edited leaf"}`)
	if got := decode[nodeJSON](t, status, body, 200); got.WorkShape == nil || got.Depth != 2 {
		t.Fatalf("write shape %+v", got)
	}
	managerless := insertPerson(t, p.TenantID, "Reader")
	if status, body = call(t, &managerless, "PUT", "/api/settings/work-vocabulary", strings.Replace(in, `"revision":0`, `"revision":1`, 1)); status != 403 || !strings.Contains(string(body), "permission denied") {
		t.Fatalf("unprivileged write %d %s", status, body)
	}
	status, body = call(t, &p, "GET", "/api/nodes?kind=work,!ticket", "")
	if len(decode[nodePage](t, status, body, 200).Items) != 0 {
		t.Fatal("retired kind exclusion did not exclude its work alias")
	}
	// A non-work child does not alter shape or the name of a leaf.
	k := kindBySlug(t, p, "project")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Subproject","parent_id":%q}`, k.ID, child.ID))
	nested := workNodeTest(t, p, project.ID, "open")
	if got := currentWorkTest(t, p, nested.ID); got.Depth != 1 || !got.IsLeaf {
		t.Fatalf("project reset %+v", got.WorkShape)
	}
	if got := currentWorkTest(t, p, child.ID); !got.IsLeaf {
		t.Fatalf("non-work child %+v", got.WorkShape)
	}
	// Hide the sole work child. The parent stays a parent, its count becomes zero,
	// and subsequent reads still cannot see that child (the RLS override restores).
	if _, err := appPool.Exec(t.Context(), `CREATE POLICY vocabulary_hidden ON nodes AS RESTRICTIVE USING(id<>'`+child.ID+`'::uuid OR aeon_visibility_system())`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := appPool.Exec(context.Background(), `DROP POLICY IF EXISTS vocabulary_hidden ON nodes`); err != nil {
			t.Error(err)
		}
	})
	if got := currentWorkTest(t, p, root.ID); got.IsLeaf || got.WorkChildrenCount != 0 || !got.StatusDerived {
		t.Fatalf("hidden child changed canonical shape %+v", got.WorkShape)
	}
	status, body = call(t, &p, "GET", "/api/nodes?ids="+child.ID, "")
	if len(decode[nodePage](t, status, body, 200).Items) != 0 {
		t.Fatal("shape read left RLS widened")
	}
}
func TestWorkShapeFilterBounds(t *testing.T) {
	for _, raw := range []string{"shape=container", "depth=0", "depth=-1", "depth=50001", "depth=no"} {
		if _, err := parseListQuery(httptest.NewRequest("GET", "/api/nodes?"+raw, nil)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	a, _ := parseListQuery(httptest.NewRequest("GET", "/api/nodes?shape=leaf&depth=2", nil))
	b, _ := parseListQuery(httptest.NewRequest("GET", "/api/nodes?shape=parent&depth=2", nil))
	if listFingerprint(a) == listFingerprint(b) {
		t.Fatal("shape lost from cursor identity")
	}
}

// AEON-791: agent names ride on the vocabulary. The risk is that a writer which
// does not know them (older clients, the Work vocabulary card) wipes them.
func TestWorkVocabularyLeadNamesSurviveLeadlessWrites(t *testing.T) {
	p := newPrincipal(t, "vocabulary-lead")
	named := `{"revision":0,"leaf":{"name":"","icon":""},"levels":[],"lead":{"singular":"Dirigent","plural":"Dirigenten"}}`
	status, body := call(t, &p, "PUT", "/api/settings/work-vocabulary", named)
	if saved := decode[workVocabulary](t, status, body, 200); saved.Lead == nil || saved.Lead.Singular != "Dirigent" || saved.Lead.Plural != "Dirigenten" {
		t.Fatalf("saved %s", body)
	}
	status, body = call(t, &p, "PUT", "/api/settings/work-vocabulary", `{"revision":1,"leaf":{"name":"Schritt","icon":""},"levels":[]}`)
	if kept := decode[workVocabulary](t, status, body, 200); kept.Leaf.Name != "Schritt" || kept.Lead == nil || kept.Lead.Plural != "Dirigenten" {
		t.Fatalf("leadless write dropped the names: %s", body)
	}
	status, body = call(t, &p, "GET", "/api/settings/work-vocabulary", "")
	if read := decode[workVocabulary](t, status, body, 200); read.Revision != 2 || read.Lead == nil || read.Lead.Singular != "Dirigent" {
		t.Fatalf("read %s", body)
	}
	for _, bad := range []string{`{"singular":" Lead","plural":""}`, `{"singular":"` + strings.Repeat("ö", 41) + `","plural":""}`, `{"singular":"A\tB","plural":""}`, `{"singular":"Lead"}`, `{"singular":"Lead","plural":"","extra":1}`} {
		in := `{"revision":2,"leaf":{"name":"Schritt","icon":""},"levels":[],"lead":` + bad + `}`
		if status, body = call(t, &p, "PUT", "/api/settings/work-vocabulary", in); status != 400 {
			t.Fatalf("accepted lead %s: %d %s", bad, status, body)
		}
	}
	// Blank names clear back to the defaults: the field is absent again.
	status, body = call(t, &p, "PUT", "/api/settings/work-vocabulary", `{"revision":2,"leaf":{"name":"Schritt","icon":""},"levels":[],"lead":{"singular":"","plural":""}}`)
	if cleared := decode[workVocabulary](t, status, body, 200); cleared.Lead != nil || strings.Contains(string(body), `"lead"`) {
		t.Fatalf("cleared %s", body)
	}
}

// The leaf and 33 parent depths: one value more than a vocabulary can name.
func tooManyLevels() string {
	values := []string{"leaf"}
	for depth := 1; depth <= 33; depth++ {
		values = append(values, fmt.Sprint(depth))
	}
	return "level=" + strings.Join(values, ",")
}
