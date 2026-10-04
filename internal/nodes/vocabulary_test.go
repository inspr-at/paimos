// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"fmt"
	"github.com/inspr-at/paimos/internal/tenant"
	"net/http/httptest"
	"strings"
	"testing"
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
	for _, bad := range []string{in + ` {}`, strings.Replace(in, `"revision":0`, `"revision":1,"unexpected":1`, 1), strings.Repeat("x", 8193)} {
		if status, _ = call(t, &p, "PUT", "/api/settings/work-vocabulary", bad); status != 400 {
			t.Fatalf("invalid body %d", status)
		}
	}
	root := workNodeTest(t, p, "", "open")
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
	for _, tc := range []struct{ filter, id string }{{"shape=parent", root.ID}, {"shape=leaf&depth=2", child.ID}, {"kind=epic&shape=leaf", child.ID}, {"shape=!parent", child.ID}} {
		status, body = call(t, &p, "GET", "/api/nodes?"+tc.filter, "")
		page := decode[nodePage](t, status, body, 200)
		if len(page.Items) != 1 || page.Items[0].ID != tc.id || page.Items[0].WorkShape == nil {
			t.Fatalf("%s: %s", tc.filter, body)
		}
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
