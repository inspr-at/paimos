// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestTicketGraphShapeClosedFilterAndIsolation(t *testing.T) {
	p := newPrincipal(t, "ticket-graph")
	projectKind := kindBySlug(t, p, "project")
	epicKind := kindBySlug(t, p, "epic")
	ticketKind := kindBySlug(t, p, "ticket")
	taskKind := kindBySlug(t, p, "task")
	memoryKind := kindBySlug(t, p, "memory")
	releaseKind := kindBySlug(t, p, "release")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Pharos"}`, projectKind.ID))
	elsewhere := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Glint"}`, projectKind.ID))
	epic := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Fleet","parent_id":%q,"state":"backlog"}`, epicKind.ID, project.ID))
	open := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Rotate keys","parent_id":%q,"state":"new","fields":{"priority":"high"},"body":"BODY-SHOULD-NOT-LEAK"}`, ticketKind.ID, epic.ID))
	doing := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"In progress","parent_id":%q,"state":"in_progress"}`, ticketKind.ID, epic.ID))
	qa := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"In QA","parent_id":%q,"state":"qa"}`, ticketKind.ID, project.ID))
	done := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Shipped","parent_id":%q,"state":"done"}`, ticketKind.ID, epic.ID))
	cancelled := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Cancelled","parent_id":%q,"state":"cancelled"}`, ticketKind.ID, project.ID))
	archived := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Archived","parent_id":%q,"state":"archived"}`, ticketKind.ID, project.ID))
	accepted := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Accepted","parent_id":%q,"state":"accepted"}`, ticketKind.ID, project.ID))
	delivered := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Delivered","parent_id":%q,"state":"delivered"}`, ticketKind.ID, project.ID))
	task := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"A task","parent_id":%q,"state":"backlog"}`, taskKind.ID, project.ID))
	memory := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"A memory","parent_id":%q}`, memoryKind.ID, project.ID))
	release := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Release 1","parent_id":%q}`, releaseKind.ID, project.ID))
	hidden := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Hidden ticket","parent_id":%q,"state":"new"}`, ticketKind.ID, elsewhere.ID))
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET updated_at=now() WHERE tenant_id=$1 AND id=$2`, p.TenantID, open.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2)`, p.TenantID, project.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number) VALUES($1,$2,$3,1)`, p.TenantID, release.ID, project.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source) VALUES($1,$2,$3,$4,0,'manual')`, p.TenantID, open.ID, project.ID, release.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	relatesSource, relatesTarget := open.ID, qa.ID
	if relatesSource > relatesTarget {
		relatesSource, relatesTarget = relatesTarget, relatesSource
	}
	insertGraphRelation(t, p, open.ID, doing.ID, "blocks")
	insertGraphRelation(t, p, relatesSource, relatesTarget, "relates")
	insertGraphRelation(t, p, open.ID, epic.ID, "implements")
	insertGraphRelation(t, p, doing.ID, qa.ID, "duplicates")
	insertGraphRelation(t, p, open.ID, qa.ID, "cites")
	insertGraphRelation(t, p, open.ID, done.ID, "blocks")
	insertGraphRelation(t, p, open.ID, hidden.ID, "blocks")
	insertGraphRelation(t, p, open.ID, memory.ID, "relates")

	status, body := call(t, &p, http.MethodGet, "/api/tickets/graph?project_id="+project.ID, "")
	g := decode[TicketGraph](t, status, body, http.StatusOK)
	if strings.Contains(string(body), "BODY-SHOULD-NOT-LEAK") || strings.Contains(string(body), `"body"`) {
		t.Fatal("graph leaks bodies")
	}
	if g.Truncated {
		t.Fatal("small graph truncated")
	}
	for _, absent := range []string{done.ID, cancelled.ID, archived.ID, accepted.ID, delivered.ID, task.ID, memory.ID, hidden.ID, elsewhere.ID} {
		if graphHasNode(g, absent) || strings.Contains(string(body), absent) {
			t.Fatalf("closed, other-kind or other-project id %s leaked", absent)
		}
	}
	if graphHasNode(g, release.ID) {
		t.Fatal("release is not a graph node")
	}
	if len(g.Nodes) != 4 { // epic, open, doing, qa
		t.Fatalf("nodes %d", len(g.Nodes))
	}
	openNode := graphNode(t, g, open.ID)
	if openNode.Key == "" || openNode.Title != "Rotate keys" || openNode.Type != "ticket" || openNode.Status != "new" || openNode.StatusCategory != "open" || openNode.Priority == nil || *openNode.Priority != "high" || openNode.ParentID == nil || *openNode.ParentID != epic.ID || openNode.ReleaseID == nil || *openNode.ReleaseID != release.ID || openNode.UpdatedAt.IsZero() {
		t.Fatalf("open node %+v", openNode)
	}
	epicNode := graphNode(t, g, epic.ID)
	if epicNode.Type != "epic" || epicNode.StatusCategory != "open" || epicNode.Priority != nil || epicNode.ParentID != nil || epicNode.ReleaseID != nil {
		t.Fatalf("epic %+v", epicNode)
	}
	if graphNode(t, g, doing.ID).StatusCategory != "doing" || graphNode(t, g, qa.ID).StatusCategory != "doing" {
		t.Fatal("doing category")
	}
	if g.Nodes[0].ID != open.ID {
		t.Fatalf("newest first: %s", g.Nodes[0].Key)
	}
	for _, link := range []TicketGraphLink{
		{Source: open.ID, Target: doing.ID, Kind: "blocks"},
		{Source: relatesSource, Target: relatesTarget, Kind: "relates"},
		{Source: open.ID, Target: epic.ID, Kind: "implements"},
		{Source: doing.ID, Target: qa.ID, Kind: "duplicates"},
		{Source: open.ID, Target: epic.ID, Kind: "parent"},
		{Source: doing.ID, Target: epic.ID, Kind: "parent"},
	} {
		if !graphHasLink(g, link) {
			t.Fatalf("missing %+v in %+v", link, g.Links)
		}
	}
	if graphHasLink(g, TicketGraphLink{Source: doing.ID, Target: open.ID, Kind: "blocks"}) || graphHasLink(g, TicketGraphLink{Source: relatesTarget, Target: relatesSource, Kind: "relates"}) {
		t.Fatal("direction was flipped")
	}
	for _, link := range g.Links {
		if link.Kind == "cites" || link.Kind == "parent" && link.Target == project.ID {
			t.Fatalf("unexpected link %+v", link)
		}
	}
	assertGraphLinkCounts(t, g)
	if openNode.LinkCount != 4 {
		t.Fatalf("open link_count %d", openNode.LinkCount)
	}

	status, body = call(t, &p, http.MethodGet, "/api/tickets/graph?project_id="+project.ID+"&include_closed=true", "")
	closed := decode[TicketGraph](t, status, body, http.StatusOK)
	for _, id := range []string{done.ID, cancelled.ID, archived.ID, accepted.ID, delivered.ID} {
		n := graphNode(t, closed, id)
		if n.StatusCategory != "done" {
			t.Fatalf("%s category %s", n.Status, n.StatusCategory)
		}
	}
	if !graphHasLink(closed, TicketGraphLink{Source: open.ID, Target: done.ID, Kind: "blocks"}) || !graphHasLink(closed, TicketGraphLink{Source: done.ID, Target: epic.ID, Kind: "parent"}) {
		t.Fatalf("closed links %+v", closed.Links)
	}
	if graphHasNode(closed, task.ID) || graphHasNode(closed, hidden.ID) || strings.Contains(string(body), hidden.ID) {
		t.Fatal("include_closed pulled in a task or another project")
	}
	assertGraphLinkCounts(t, closed)

	status, body = call(t, &p, http.MethodGet, "/api/tickets/graph?project_id="+elsewhere.ID, "")
	other := decode[TicketGraph](t, status, body, http.StatusOK)
	if len(other.Nodes) != 1 || other.Nodes[0].ID != hidden.ID || strings.Contains(string(body), open.ID) {
		t.Fatalf("other project %+v", other)
	}

	guest := tenant.Principal{TenantID: p.TenantID, Kind: tenant.Person, Name: "Guest"}
	if err := testDB.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Guest') RETURNING id::text`, p.TenantID).Scan(&guest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, p.TenantID, guest.ID, project.ID); err != nil {
		t.Fatal(err)
	}
	if err := authz.RequirePattern(authz.BindPool(tenant.WithPrincipal(t.Context(), guest), appPool), "GET /api/tickets/graph", authz.Scope{}); err == nil {
		t.Fatal("guest workspace scope authorized the graph")
	}
	if !authz.ProjectFilteredRoutes["GET /api/tickets/graph"] {
		t.Fatal("ticket graph is not a project-filtered route")
	}
	if err := authz.RequirePattern(authz.BindPool(tenant.WithPrincipal(t.Context(), guest), appPool), "GET /api/tickets/graph", authz.Scope{AnyProject: true}); err != nil {
		t.Fatal(err)
	}
	status, body = call(t, &guest, http.MethodGet, "/api/tickets/graph?project_id="+project.ID, "")
	guestGraph := decode[TicketGraph](t, status, body, http.StatusOK)
	if strings.Contains(string(body), hidden.ID) || graphHasNode(guestGraph, hidden.ID) {
		t.Fatal("cross-project relation visible to a guest")
	}
	if !graphHasNode(guestGraph, open.ID) || !graphHasLink(guestGraph, TicketGraphLink{Source: open.ID, Target: doing.ID, Kind: "blocks"}) {
		t.Fatal("guest lost the project they can see")
	}
	if status, _ := call(t, &guest, http.MethodGet, "/api/tickets/graph?project_id="+elsewhere.ID, ""); status != http.StatusNotFound {
		t.Fatalf("guest other project %d", status)
	}
	foreign := addPrincipal(t, "ticket-graph-foreign")
	if status, _ := call(t, &foreign, http.MethodGet, "/api/tickets/graph?project_id="+project.ID, ""); status != http.StatusNotFound {
		t.Fatalf("foreign tenant %d", status)
	}
	for _, path := range []string{"", "?project_id=bad", "?project_id=" + project.ID + "&include_closed=yes"} {
		if status, _ := call(t, &p, http.MethodGet, "/api/tickets/graph"+path, ""); status != http.StatusBadRequest {
			t.Fatalf("%s: %d", path, status)
		}
	}
	if status, _ := call(t, nil, http.MethodGet, "/api/tickets/graph?project_id="+project.ID, ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous %d", status)
	}
}

func TestTicketGraphCap(t *testing.T) {
	if ticketGraphNodeLimit != 1500 {
		t.Fatalf("cap %d", ticketGraphNodeLimit)
	}
	p := newPrincipal(t, "ticket-graph-cap")
	projectKind := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "ticket")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Large"}`, projectKind.ID))
	ids := map[string]string{}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,state,parent_id,updated_at,body)
 SELECT $1,'GRF-'||g,$2::uuid,'Graph '||g,'backlog',$3::uuid, now() - make_interval(secs => g), 'BODY-SHOULD-NOT-LEAK'
 FROM generate_series(1,1500) g
 RETURNING key, id::text`, p.TenantID, ticketKind.ID, project.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key, id string
			if err = rows.Scan(&key, &id); err != nil {
				rows.Close()
				return err
			}
			ids[key] = id
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		var doneID string
		if err = tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,state,parent_id,updated_at,body)
 VALUES($1,'GRF-1501',$2::uuid,'Newest done','done',$3::uuid, now(), 'BODY-SHOULD-NOT-LEAK') RETURNING id::text`, p.TenantID, ticketKind.ID, project.ID).Scan(&doneID); err != nil {
			return err
		}
		ids["GRF-1501"] = doneID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1501 {
		t.Fatalf("inserted %d", len(ids))
	}
	insertGraphRelation(t, p, ids["GRF-1501"], ids["GRF-1"], "blocks")
	relateFrom, relateTo := ids["GRF-1"], ids["GRF-1500"]
	if relateFrom > relateTo {
		relateFrom, relateTo = relateTo, relateFrom
	}
	insertGraphRelation(t, p, relateFrom, relateTo, "relates")

	status, body := call(t, &p, http.MethodGet, "/api/tickets/graph?project_id="+project.ID, "")
	g := decode[TicketGraph](t, status, body, http.StatusOK)
	if len(g.Nodes) != 1500 || g.Truncated || strings.Contains(string(body), "BODY-SHOULD-NOT-LEAK") || strings.Contains(string(body), ids["GRF-1501"]) || !strings.Contains(string(body), ids["GRF-1500"]) {
		t.Fatalf("default cap nodes=%d truncated=%v", len(g.Nodes), g.Truncated)
	}
	if graphHasLink(g, TicketGraphLink{Source: ids["GRF-1501"], Target: ids["GRF-1"], Kind: "blocks"}) || !graphHasLink(g, TicketGraphLink{Source: relateFrom, Target: relateTo, Kind: "relates"}) {
		t.Fatal("default links")
	}
	status, body = call(t, &p, http.MethodGet, "/api/tickets/graph?project_id="+project.ID+"&include_closed=true", "")
	g = decode[TicketGraph](t, status, body, http.StatusOK)
	if len(g.Nodes) != 1500 || !g.Truncated || !graphHasNode(g, ids["GRF-1501"]) || graphHasNode(g, ids["GRF-1500"]) || strings.Contains(string(body), ids["GRF-1500"]) {
		t.Fatalf("closed cap nodes=%d truncated=%v", len(g.Nodes), g.Truncated)
	}
	if !graphHasLink(g, TicketGraphLink{Source: ids["GRF-1501"], Target: ids["GRF-1"], Kind: "blocks"}) || graphHasLink(g, TicketGraphLink{Source: relateFrom, Target: relateTo, Kind: "relates"}) {
		t.Fatal("closed cap links")
	}
	if graphNode(t, g, ids["GRF-1501"]).StatusCategory != "done" {
		t.Fatal("capped done category")
	}
}

func TestTicketGraphQueryCount(t *testing.T) {
	p := newPrincipal(t, "ticket-graph-queries")
	projectKind := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "ticket")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Queries"}`, projectKind.ID))
	insertGraphTickets(t, p, ticketKind.ID, project.ID, 1, 10)
	tracer := &ticketGraphTracer{}
	pool := tracedPool(t, tracer)
	mod := New(pool, nil)
	one := tracer.snapshot()
	status, body := callAs(t, mod, &p, http.MethodGet, "/api/tickets/graph?project_id="+project.ID, "")
	decode[TicketGraph](t, status, body, http.StatusOK)
	small := tracer.snapshot()
	insertGraphTickets(t, p, ticketKind.ID, project.ID, 11, 40)
	status, body = callAs(t, mod, &p, http.MethodGet, "/api/tickets/graph?project_id="+project.ID, "")
	g := decode[TicketGraph](t, status, body, http.StatusOK)
	large := tracer.snapshot()
	if len(g.Nodes) != 40 {
		t.Fatalf("nodes %d", len(g.Nodes))
	}
	for _, marker := range []string{"ticket-graph-project", "ticket-graph-nodes", "ticket-graph-links"} {
		if n := countMarker(small, marker) - countMarker(one, marker); n != 1 {
			t.Fatalf("%s ran %d times for 10 nodes", marker, n)
		}
		if n := countMarker(large, marker) - countMarker(small, marker); n != 1 {
			t.Fatalf("%s ran %d times for 40 nodes", marker, n)
		}
	}
	nodesSQL := sqlWithMarker(large[len(small):], "ticket-graph-nodes")
	if nodesSQL.SQL == "" || strings.Contains(nodesSQL.SQL, "body") {
		t.Fatalf("node sql %q", nodesSQL.SQL)
	}
	var plan strings.Builder
	err := db.InTenant(tenant.WithPrincipal(t.Context(), p), appPool, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS) "+nodesSQL.SQL, nodesSQL.Args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan.WriteString(line)
			plan.WriteByte('\n')
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	text := plan.String()
	first, _, _ := strings.Cut(text, "\n")
	if !strings.Contains(first, "Limit") || !strings.Contains(first, "loops=1") {
		t.Fatalf("node plan did not execute once:\n%s", text)
	}
}

func TestTicketGraphAgentKeyScope(t *testing.T) {
	p := newPrincipal(t, "ticket-graph-agent")
	projectKind := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "ticket")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Agent"}`, projectKind.ID))
	ticket := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Visible","parent_id":%q,"state":"new"}`, ticketKind.ID, project.ID))
	mod, err := auth.New(auth.Config{SessionKey: bytes.Repeat([]byte{7}, 32)}, appPool)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&httpapi.Server{Pool: appPool, Modules: []httpapi.Module{mod, New(appPool, nil)}, Middleware: []func(http.Handler) http.Handler{mod.Middleware}}).Handler()
	key := func(name string, scopes []string) string {
		t.Helper()
		_, _, token, err := auth.OperatorCreateAgentKey(t.Context(), appPool, p.TenantID, name, "", scopes, nil)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	reader := key("graph reader", []string{"nodes.read"})
	other := key("graph other", []string{"knowledge.read"})
	empty := key("graph empty", nil)
	callKey := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/tickets/graph?project_id="+project.ID, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	ok := callKey(reader)
	g := decode[TicketGraph](t, ok.Code, ok.Body.Bytes(), http.StatusOK)
	if !graphHasNode(g, ticket.ID) {
		t.Fatal("nodes.read key did not see the ticket")
	}
	if callKey(other).Code != http.StatusForbidden || callKey(empty).Code != http.StatusForbidden {
		t.Fatal("a key without nodes.read reached the ticket graph")
	}
}

func insertGraphRelation(t *testing.T, p tenant.Principal, source, target, kind string) {
	t.Helper()
	if kind == "relates" && source > target {
		source, target = target, source
	}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,$4)`, p.TenantID, source, target, kind); err != nil {
			return err
		}
		_, err := events.Append(t.Context(), tx, p, events.Change{Type: "relation.created", After: map[string]string{"source_node_id": source, "target_node_id": target, "type": kind}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertGraphTickets(t *testing.T, p tenant.Principal, kindID, projectID string, from, to int) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,state,parent_id)
 SELECT $1,'QRY-'||g,$2::uuid,'Query '||g,'backlog',$3::uuid FROM generate_series($4::int,$5::int) g`, p.TenantID, kindID, projectID, from, to)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func graphHasNode(g TicketGraph, id string) bool {
	for _, n := range g.Nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

func graphNode(t *testing.T, g TicketGraph, id string) TicketGraphNode {
	t.Helper()
	for _, n := range g.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("missing node %s", id)
	return TicketGraphNode{}
}

func graphHasLink(g TicketGraph, want TicketGraphLink) bool {
	for _, link := range g.Links {
		if link == want {
			return true
		}
	}
	return false
}

func assertGraphLinkCounts(t *testing.T, g TicketGraph) {
	t.Helper()
	counts := map[string]int{}
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	for _, link := range g.Links {
		if !ids[link.Source] || !ids[link.Target] || link.Source == link.Target {
			t.Fatalf("link outside the graph %+v", link)
		}
		counts[link.Source]++
		counts[link.Target]++
	}
	for _, n := range g.Nodes {
		if n.LinkCount != counts[n.ID] {
			t.Fatalf("%s link_count %d want %d", n.Key, n.LinkCount, counts[n.ID])
		}
	}
}

type ticketGraphTracer struct {
	mu    sync.Mutex
	calls []pgx.TraceQueryStartData
}

func (t *ticketGraphTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	t.mu.Lock()
	t.calls = append(t.calls, data)
	t.mu.Unlock()
	return ctx
}

func (t *ticketGraphTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (t *ticketGraphTracer) snapshot() []pgx.TraceQueryStartData {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]pgx.TraceQueryStartData(nil), t.calls...)
}

func tracedPool(t *testing.T, tracer *ticketGraphTracer) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(testDB.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 2
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func countMarker(calls []pgx.TraceQueryStartData, marker string) int {
	n := 0
	for _, call := range calls {
		if strings.Contains(call.SQL, marker) {
			n++
		}
	}
	return n
}

func sqlWithMarker(calls []pgx.TraceQueryStartData, marker string) pgx.TraceQueryStartData {
	for _, call := range calls {
		if strings.Contains(call.SQL, marker) {
			return call
		}
	}
	return pgx.TraceQueryStartData{}
}
