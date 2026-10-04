// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
)

func TestGraphMentions(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		want       []graphMention
	}{
		{"wiki", "[[adr-001-foundation]] [[adr-001-foundation|Foundation]] [[deploy#Steps]]", []graphMention{{Slug: "adr-001-foundation"}, {Slug: "deploy"}}},
		{"typed path", "[Guide](/p/AEON/knowledge/external-system/console#login)", []graphMention{{Slug: "console", Type: "external_system", Project: "AEON"}}},
		{"encoded path", "[Guide](/p/PRJ%2D1/knowledge/memory/adr-001)", []graphMention{{Slug: "adr-001", Type: "memory", Project: "PRJ-1"}}},
		{"code and keys", "Use `adr-001-foundation` for AEON-3 and AEON-3, not XAEON-3-extra or /AEON-2.", []graphMention{{Slug: "adr-001-foundation"}, {Key: "AEON-3"}}},
		{"unresolved and invalid", "[[no-such-entry]] [[Bad Slug]] [x](https://elsewhere.test/p/AEON/knowledge/memory/secret) `not known`", []graphMention{{Slug: "no-such-entry"}}},
		{"nothing", "plain prose", []graphMention{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseGraphMentions(tt.body); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v want %#v", got, tt.want)
			}
		})
	}
}

func graphCall(t *testing.T, f fixture, query string) Graph {
	t.Helper()
	w := call(t, f, f.a, "GET", "/api/knowledge/graph?project_id="+f.project+query, nil)
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), `"body"`) {
		t.Fatal("graph leaks bodies")
	}
	return decode[Graph](t, w)
}
func graphHas(g Graph, source, target, kind string) bool {
	for _, e := range g.Edges {
		if e.Source == source && e.Target == target && e.Kind == kind {
			return true
		}
	}
	return false
}

func TestHistoricalSlugFollowsRenameOrderAfterBodyEdit(t *testing.T) {
	f := setup(t)
	a := createEntry(t, f, f.a, map[string]any{"type": "memory", "slug": "reused", "title": "First"})
	expect(t, call(t, f, f.a, "PATCH", "/api/knowledge/"+a.ID, map[string]any{"slug": "first"}), 200)
	b := createEntry(t, f, f.a, map[string]any{"type": "memory", "slug": "reused", "title": "Second"})
	expect(t, call(t, f, f.a, "PATCH", "/api/knowledge/"+b.ID, map[string]any{"slug": "second"}), 200)
	source := createEntry(t, f, f.a, map[string]any{"type": "memory", "slug": "referrer", "title": "Referrer", "body": "[[reused]]"})
	check := func() {
		t.Helper()
		w := call(t, f, f.a, "GET", "/api/knowledge/resolve?project_id="+f.project+"&type=memory&slug=reused", nil)
		expect(t, w, 200)
		if got := decode[Entry](t, w); got.ID != b.ID {
			t.Errorf("alias retargeted: got %s want %s", got.ID, b.ID)
		}
		g := graphCall(t, f, "")
		if !graphHas(g, source.ID, b.ID, "mention") || graphHas(g, source.ID, a.ID, "mention") {
			t.Error("graph alias retargeted")
		}
	}
	check()
	expect(t, call(t, f, f.a, "PATCH", "/api/knowledge/"+a.ID, map[string]any{"body": "An unrelated edit."}), 200)
	// Set explicit timestamps: no dependence on clock resolution or sleeps.
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET updated_at=CASE WHEN id=$1::uuid THEN '2030-01-02'::timestamptz ELSE '2030-01-01'::timestamptz END WHERE id IN ($1::uuid,$2::uuid)`, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	check()
}
func TestGraphResolutionRelationsFiltersAndIsolation(t *testing.T) {
	f := setup(t)
	target := createEntry(t, f, f.a, map[string]any{"type": "memory", "slug": "adr-old", "title": "Foundation"})
	same := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "shared", "title": "Runbook"})
	other := createEntry(t, f, f.a, map[string]any{"type": "memory", "slug": "shared", "title": "Memory"})
	archived := createEntry(t, f, f.a, map[string]any{"type": "memory", "slug": "archived-entry", "title": "History", "status": "archived"})
	foreign := createEntry(t, f, f.foreign, map[string]any{"project_id": f.elsewhere, "type": "memory", "slug": "foreign", "title": "Private"})
	elsewhere := createEntry(t, f, f.a, map[string]any{"project_id": f.other, "type": "memory", "slug": "elsewhere", "title": "Other project"})
	source := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "source", "title": "Source", "body": fmt.Sprintf("[[shared]] [[source]] [[foreign]] [[elsewhere]] [[archived-entry]] `adr-old` %s PHAROS-7 [typed](/p/PRJ-1/knowledge/memory/shared) [other](/p/PRJ-2/knowledge/memory/shared)", same.Key)})
	// Real rename event, including its before snapshot, is written by PATCH.
	expect(t, call(t, f, f.a, "PATCH", "/api/knowledge/"+target.ID, map[string]any{"slug": "adr-new"}), 200)
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		for _, pair := range [][2]string{{source.ID, same.ID}, {f.ticket, target.ID}} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'cites')`, f.a.TenantID, pair[0], pair[1]); err != nil {
				return err
			}
			if _, err := events.Append(t.Context(), tx, f.a, events.Change{Type: "relation.created", After: map[string]string{"source_node_id": pair[0], "target_node_id": pair[1]}}); err != nil {
				return err
			}
		}
		// Ticket body mentions must never become extra satellites/edges.
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET body='[[shared]]' WHERE tenant_id=$1 AND id=$2`, f.a.TenantID, f.ticket)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	g := graphCall(t, f, "")
	if len(g.Nodes) != 5 || len(g.Edges) != 4 || g.Truncated {
		t.Fatalf("unexpected graph: %+v", g)
	}
	for _, edge := range [][3]string{{source.ID, same.ID, "relation"}, {source.ID, other.ID, "mention"}, {source.ID, target.ID, "mention"}, {source.ID, archived.ID, "mention"}} {
		if !graphHas(g, edge[0], edge[1], edge[2]) {
			t.Fatalf("missing edge %v", edge)
		}
	}
	for _, n := range g.Nodes {
		if n.ID == source.ID && n.Degree != 4 {
			t.Fatalf("degree=%d", n.Degree)
		}
		if n.ID == foreign.ID || n.ID == elsewhere.ID || n.ID == f.ticket {
			t.Fatal("out of scope node")
		}
	}
	g = graphCall(t, f, "&include=tickets&status=active")
	if len(g.Nodes) != 5 || len(g.Edges) != 5 || !graphHas(g, source.ID, f.ticket, "mention") || !graphHas(g, f.ticket, target.ID, "relation") {
		t.Fatalf("satellites: %+v", g)
	}
	if graphHas(g, f.ticket, other.ID, "mention") {
		t.Fatal("read ticket body")
	}
	g = graphCall(t, f, "&types=runbook")
	if len(g.Nodes) != 2 || len(g.Edges) != 1 {
		t.Fatalf("filter: %+v", g)
	}
	// Neither a project ID nor a body mention can cross the tenant boundary.
	expect(t, call(t, f, f.foreign, "GET", "/api/knowledge/graph?project_id="+f.project, nil), 404)
	expect(t, call(t, f, f.a, "GET", "/api/knowledge/graph?project_id="+f.elsewhere, nil), 404)
	for _, q := range []string{"", "?project_id=bad", "?project_id=" + f.project + "&types=secret", "?project_id=" + f.project + "&status=done", "?project_id=" + f.project + "&include=body"} {
		expect(t, call(t, f, f.a, "GET", "/api/knowledge/graph"+q, nil), 400)
	}
}

func TestGraphCaps(t *testing.T) {
	f := setup(t)
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,fields)
   SELECT $1,'RUN-'||s,k.id,'Runbook '||s,$2,jsonb_build_object('slug','run-'||s)
   FROM generate_series(1,2001) s CROSS JOIN node_kinds k WHERE k.tenant_id=$1 AND k.slug='runbook'`, f.a.TenantID, f.project)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,fields)
   SELECT $1,'MEM-'||s,k.id,'Memory '||s,$2,jsonb_build_object('slug','memory-'||s)
   FROM generate_series(1,91) s CROSS JOIN node_kinds k WHERE k.tenant_id=$1 AND k.slug='memory'`, f.a.TenantID, f.project)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type)
   SELECT $1,a.id,b.id,'cites' FROM nodes a CROSS JOIN nodes b
   WHERE a.tenant_id=$1 AND b.tenant_id=$1 AND a.key LIKE 'MEM-%' AND b.key LIKE 'MEM-%' AND a.id<>b.id`, f.a.TenantID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	g := graphCall(t, f, "&types=runbook")
	if len(g.Nodes) != 2000 || !g.Truncated {
		t.Fatalf("node cap: %d truncated=%v", len(g.Nodes), g.Truncated)
	}
	g = graphCall(t, f, "&types=memory")
	if len(g.Nodes) != 91 || len(g.Edges) != 8000 || !g.Truncated {
		t.Fatalf("edge cap: %d/%d truncated=%v", len(g.Nodes), len(g.Edges), g.Truncated)
	}
	seen := map[[2]string]bool{}
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	for _, e := range g.Edges {
		pair := [2]string{e.Source, e.Target}
		if e.Source == e.Target || seen[pair] || !ids[e.Source] || !ids[e.Target] {
			t.Fatal("invalid capped edge")
		}
		seen[pair] = true
	}
}

func TestGraphTicketSatellitesIncludeRelationsWithoutExpandingHops(t *testing.T) {
	f := setup(t)
	source := createEntry(t, f, f.a, map[string]any{"type": "memory", "slug": "source", "title": "Source", "body": "PHAROS-7 PHAROS-8"})
	var satellite, distant string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		// Synthetic fixture bodies deliberately name the next ticket. Graph
		// expansion must only inspect the knowledge body's references.
		for i, id := range []*string{&satellite, &distant} {
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,body)
 SELECT $1,$2,id,'Satellite',$3,'PHAROS-9' FROM node_kinds WHERE tenant_id=$1 AND slug='ticket' RETURNING id::text`, f.a.TenantID, fmt.Sprintf("PHAROS-%d", i+8), f.other).Scan(id); err != nil {
				return err
			}
		}
		for _, pair := range [][2]string{{f.ticket, satellite}, {satellite, distant}} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'cites')`, f.a.TenantID, pair[0], pair[1]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	g := graphCall(t, f, "&include=tickets")
	if len(g.Nodes) != 3 || len(g.Edges) != 3 || !graphHas(g, f.ticket, satellite, "relation") || !graphHas(g, source.ID, satellite, "mention") {
		t.Fatalf("satellite relations: %+v", g)
	}
	for _, n := range g.Nodes {
		if n.ID == distant {
			t.Fatal("ticket body or relation expanded a second hop")
		}
	}
}

func TestGraphResolutionPriorityAndDeletedHistory(t *testing.T) {
	f := setup(t)
	old := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "old-name", "title": "Renamed"})
	createEntry(t, f, f.a, map[string]any{"type": "memory", "slug": "old-name", "title": "Other kind"})
	hidden := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "hidden", "title": "Archived", "status": "archived"})
	other := createEntry(t, f, f.a, map[string]any{"type": "memory", "slug": "hidden", "title": "Different kind"})
	source := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "source", "title": "Source", "body": "[[old-name]] [[hidden]]"})
	expect(t, call(t, f, f.a, "PATCH", "/api/knowledge/"+old.ID, map[string]any{"slug": "new-name"}), 200)
	g := graphCall(t, f, "&status=active")
	if !graphHas(g, source.ID, old.ID, "mention") || graphHas(g, source.ID, other.ID, "mention") || graphHas(g, source.ID, hidden.ID, "mention") {
		t.Fatalf("same-kind and visibility priority: %+v", g.Edges)
	}
	reused := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "old-name", "title": "Reused"})
	g = graphCall(t, f, "")
	if !graphHas(g, source.ID, reused.ID, "mention") || graphHas(g, source.ID, old.ID, "mention") {
		t.Fatal("live slug must win over historical alias")
	}
	expect(t, call(t, f, f.a, "DELETE", "/api/knowledge/"+reused.ID, nil), 200)
	expect(t, call(t, f, f.a, "DELETE", "/api/knowledge/"+old.ID, nil), 200)
	g = graphCall(t, f, "")
	for _, n := range g.Nodes {
		if n.ID == old.ID || n.ID == reused.ID {
			t.Fatal("deleted entries reappeared through history")
		}
	}
}
