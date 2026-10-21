// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: an individual read grant leaks its project, becomes write authority,
// or escapes the key creator/person intersection through mixed scope types.
func TestNodeGrantVisibility(t *testing.T) {
	w := newMatrixWorld(t)
	ctx := t.Context()
	person := tenant.Principal{TenantID: w.tid, Kind: tenant.Person}
	if err := w.d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Node recipient') RETURNING id::text`, w.tid).Scan(&person.ID); err != nil {
		t.Fatal(err)
	}
	bind := func(principal, scope, id, role string) {
		t.Helper()
		if _, err := w.d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,$3,$4 FROM roles WHERE tenant_id=$1 AND key=$5`, w.tid, principal, scope, id, role); err != nil {
			t.Fatal(err)
		}
	}
	// A member role contains writes; at node scope even that role adds reads only.
	bind(person.ID, "node", w.ticketA, "member")
	wide := w.people["agent, workspace key"]
	wide.KeyCreatorID, wide.KeyPersonID, wide.FullAccess = w.people["owner"].ID, person.ID, true
	capped := wide
	capped.KeyCreatorID = w.people["customer"].ID
	projectCreator := wide
	projectCreator.KeyCreatorID = w.people["guest"].ID
	projectAgent := w.people["agent, project-scoped"]
	projectAgent.KeyCreatorID, projectAgent.KeyPersonID = wide.KeyCreatorID, person.ID
	nodeAgent := w.people["agent, no binding"]
	bind(nodeAgent.ID, "node", w.ticketA, "viewer")
	nodeAgent.KeyCreatorID, nodeAgent.KeyPersonID = wide.KeyCreatorID, w.people["guest"].ID
	invalidPerson := wide
	invalidPerson.KeyPersonID = nodeAgent.ID
	bind(w.people["customer"].ID, "node", w.knowledgeA, "customer")
	alias := person
	if err := w.d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,linked_to) VALUES($1,'person','Alias',$2) RETURNING id::text`, w.tid, person.ID).Scan(&alias.ID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		p    tenant.Principal
		want bool
	}{
		{"person", person, true}, {"canonical alias", alias, true},
		{"person bound agent", wide, true}, {"creator without grant", capped, false},
		{"project creator intersects person node", projectCreator, true},
		{"project agent intersects person node", projectAgent, true},
		{"node agent intersects person project", nodeAgent, true},
		{"acting person must be a person", invalidPerson, false},
		{"role without nodes.read", w.people["customer"], false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := w.view(t, tenant.WithPrincipal(ctx, tc.p))
			wantNodes, wantCount := []string(nil), 0
			if tc.want {
				wantNodes, wantCount = []string{"TA-1"}, 1
			}
			if !slices.Equal(got.nodes, wantNodes) || got.attachments != wantCount || got.comments != wantCount || got.search != wantCount || got.journey != 0 || got.relations != 0 || len(got.knowledge) != 0 || got.workspaceEvents != 0 {
				t.Fatalf("node grant view %+v; want nodes %v and %d node-owned rows", got, wantNodes, wantCount)
			}
		})
	}
	// A server-resolved run uses the same entry and therefore the same ceiling.
	var run string
	err := db.InTenant(dbtest.Seed(ctx), w.d.App, w.tid, func(tx pgx.Tx) error {
		var order string
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,'WOR-1','Order',$2 FROM node_kinds WHERE tenant_id=$1 AND slug='work_order' RETURNING id::text`, w.tid, w.projectA).Scan(&order); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES($1,$2,$3)`, w.tid, order, person.ID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,acting_person_id) VALUES($1,$2,$3,$4) RETURNING id::text`, w.tid, order, wide.ID, person.ID).Scan(&run)
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy := wide
	legacy.KeyPersonID = ""
	err = db.InTenant(tenant.WithPrincipal(ctx, legacy), w.d.App, w.tid, func(tx pgx.Tx) error {
		resolved, err := db.RunPrincipalTx(ctx, tx, legacy, run)
		if err != nil {
			return err
		}
		if err := db.EnterPrincipal(ctx, tx, w.tid, resolved.ID, resolved.KeyCreatorID, db.ActingPersonID(ctx, resolved)); err != nil {
			return err
		}
		var ids []string
		if err := tx.QueryRow(ctx, `SELECT array_agg(key ORDER BY key) FROM nodes`).Scan(&ids); err != nil {
			return err
		}
		if !slices.Equal(ids, []string{"TA-1"}) {
			t.Fatalf("run sees %v", ids)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(tenant.WithPrincipal(ctx, person), w.d.App, w.tid, func(tx pgx.Tx) error {
		var denied *denial
		if err := RequireTx(ctx, tx, person, "nodes.write", Scope{ProjectID: w.projectA}); !errors.Is(err, ErrForbidden) || !errors.As(err, &denied) || denied.reason != "missing_project_access" {
			t.Fatalf("node grant authorized write, or wrong error: %v", err)
		}
		// Re-entry must remove a previous principal's individual grants too.
		if err := db.EnterPrincipal(ctx, tx, w.tid, w.people["customer"].ID, nil, nil); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM nodes`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("re-entry retained %d nodes", count)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A node grant is additive alongside an ordinary project binding.
	bind(person.ID, "project", w.projectB, "viewer")
	if got := w.view(t, tenant.WithPrincipal(ctx, person)); !slices.Equal(got.nodes, []string{"GB-1", "PB-1", "TA-1", "TB-1"}) {
		t.Fatalf("project/node union: %+v", got)
	}
	if _, err := w.d.Admin.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1 AND scope_type='project'`, person.ID); err != nil {
		t.Fatal(err)
	}
	// Explicit sharing must still obey both independent restrictive policies.
	var rule, outcome string
	err = db.InTenant(dbtest.Seed(ctx), w.d.App, w.tid, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.rules_write','on',true),set_config('aeon.rules_access','on',true)`); err != nil {
			return err
		}
		for _, slug := range []string{"aeon_rule", "outcome"} {
			if _, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,$2,$2,$3,'goal')`, w.tid, slug, map[string]string{"aeon_rule": "RUL", "outcome": "OUT"}[slug]); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,fields) SELECT $1,id,'RUL-1','Private rule',$2,'{"_aeon_rule_resource":"rule","scope":{"layer":"company"}}'::jsonb FROM node_kinds WHERE tenant_id=$1 AND slug='aeon_rule' RETURNING id::text`, w.tid, w.projectA).Scan(&rule); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,'OUT-1','Hidden outcome',$2 FROM node_kinds WHERE tenant_id=$1 AND slug='outcome' RETURNING id::text`, w.tid, w.projectA).Scan(&outcome); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'serves')`, w.tid, outcome, w.ticketB)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	bind(person.ID, "node", rule, "viewer")
	bind(person.ID, "node", outcome, "viewer")
	for _, p := range []tenant.Principal{person, wide} {
		if got := w.view(t, tenant.WithPrincipal(ctx, p)); !slices.Equal(got.nodes, []string{"TA-1"}) {
			t.Fatalf("node grant bypassed rule/outcome policy: %+v", got)
		}
	}
	if _, err := w.d.Admin.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1`, person.ID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []tenant.Principal{person, alias, wide} {
		if got := w.view(t, tenant.WithPrincipal(ctx, p)); len(got.nodes) != 0 {
			t.Fatalf("inactive recipient retains nodes: %+v", got)
		}
	}
	if _, err := w.d.Admin.Exec(ctx, `UPDATE principals SET status='active' WHERE id=$1`, person.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Admin.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, person.ID); err != nil {
		t.Fatal(err)
	}
	if got := w.view(t, tenant.WithPrincipal(ctx, wide)); len(got.nodes) != 0 {
		t.Fatalf("revoked grant retained by agent: %+v", got)
	}
}

type matrixWorld struct {
	d                      *dbtest.DB
	tid                    string
	projectA, projectB     string
	ticketA, ticketB       string
	knowledgeA, knowledgeB string
	org                    string
	people                 map[string]tenant.Principal
	bootstrapEvents        int
}

// The project data every matrix row reads: nodes, knowledge, relations,
// comments, attachments, journey, search and workspace events.
type matrixView struct {
	nodes, knowledge                                  []string
	relations, comments, attachments, journey, search int
	workspaceEvents                                   int
	visibility                                        string
}

func newMatrixWorld(t *testing.T) *matrixWorld {
	t.Helper()
	w := &matrixWorld{d: dbtest.Open(t), people: map[string]tenant.Principal{}}
	ctx := dbtest.Seed(t.Context())
	if err := w.d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('p2-matrix','P2 matrix') RETURNING id::text`).Scan(&w.tid); err != nil {
		t.Fatal(err)
	}
	if err := w.d.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1 AND node_id IS NULL`, w.tid).Scan(&w.bootstrapEvents); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(ctx, w.d.App, w.tid, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,'organisation','Organisation','ORG','organisation')`, w.tid); err != nil {
			return err
		}
		node := func(kind, key string, parent *string) string {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,body,parent_id) SELECT $1,id,$3,'zebra '||$3,'zebra body',$4 FROM node_kinds WHERE tenant_id=$1 AND slug=$2 RETURNING id::text`, w.tid, kind, key, parent).Scan(&id); err != nil {
				t.Fatalf("node %s: %v", key, err)
			}
			return id
		}
		w.projectA, w.projectB = node("project", "PA-1", nil), node("project", "PB-1", nil)
		w.ticketA, w.ticketB = node("work", "TA-1", &w.projectA), node("work", "TB-1", &w.projectB)
		w.knowledgeA, w.knowledgeB = node("guideline", "GA-1", &w.projectA), node("guideline", "GB-1", &w.projectB)
		w.org = node("organisation", "ORG-1", nil)
		var writer string
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Writer') RETURNING id::text`, w.tid).Scan(&writer); err != nil {
			return err
		}
		for _, rel := range [][2]string{{w.ticketA, w.ticketB}, {w.ticketA, w.knowledgeA}} {
			if _, err := tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'cites')`, w.tid, rel[0], rel[1]); err != nil {
				return err
			}
		}
		for _, n := range []string{w.ticketA, w.ticketB} {
			if _, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after) VALUES($1,$2,$3,'comment.created','{"body_markdown":"hi"}')`, w.tid, writer, n); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO attachments(tenant_id,node_id,sha256,name,content_type,size,created_by) VALUES($1,$2,repeat('a',64),'a.txt','text/plain',1,$3)`, w.tid, n, writer); err != nil {
				return err
			}
		}
		for _, p := range []string{w.projectA, w.projectB} {
			if _, err := tx.Exec(ctx, `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2)`, w.tid, p); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,type,after) VALUES($1,$2,'kind.created','{"workspace":true}')`, w.tid, writer)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	add := func(name, kind string) tenant.Principal {
		p := tenant.Principal{TenantID: w.tid, Kind: tenant.PrincipalKind(kind), Name: name}
		if err := w.d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,$3) RETURNING id::text`, w.tid, kind, name).Scan(&p.ID); err != nil {
			t.Fatal(err)
		}
		w.people[name] = p
		return p
	}
	for _, role := range []string{"owner", "admin", "member", "viewer", "customer"} {
		dbtest.BindRole(t, w.d, w.tid, add(role, "person").ID, role)
	}
	bindProject := func(p tenant.Principal, role, project string) {
		if _, err := w.d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key=$4`, w.tid, p.ID, project, role); err != nil {
			t.Fatal(err)
		}
	}
	guest := add("guest", "person")
	bindProject(guest, "guest", w.projectA)
	gone := add("deactivated guest", "person")
	bindProject(gone, "guest", w.projectA)
	if _, err := w.d.Admin.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1`, gone.ID); err != nil {
		t.Fatal(err)
	}
	// Agents: a workspace role that reads nodes, a project-scoped agent, and
	// a key whose creator is a project guest (capped to the creator's view).
	var agentRole string
	if err := w.d.Admin.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'matrix_agent','Matrix agent') RETURNING id::text`, w.tid).Scan(&agentRole); err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read'),($1,$2,'search.read')`, w.tid, agentRole); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent, workspace key", "agent, key from project guest"} {
		p := add(name, "agent")
		if _, err := w.d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, w.tid, p.ID, agentRole); err != nil {
			t.Fatal(err)
		}
	}
	capped := w.people["agent, key from project guest"]
	capped.KeyCreatorID = guest.ID
	w.people["agent, key from project guest"] = capped
	bindProject(add("agent, project-scoped", "agent"), "member", w.projectA)
	add("agent, no binding", "agent")
	// The guest's own workspace activity stays readable to the guest.
	if _, err := w.d.Admin.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,type,after) VALUES($1,$2,'profile.updated','{"self":true}')`, w.tid, guest.ID); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *matrixWorld) view(t *testing.T, ctx context.Context) matrixView {
	t.Helper()
	var v matrixView
	err := db.InTenant(ctx, w.d.App, w.tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&v.visibility); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT n.key,k.slug FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id ORDER BY n.key`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key, kind string
			if err := rows.Scan(&key, &kind); err != nil {
				rows.Close()
				return err
			}
			v.nodes = append(v.nodes, key)
			if kind == "guideline" {
				v.knowledge = append(v.knowledge, key)
			}
		}
		rows.Close()
		return tx.QueryRow(ctx, `SELECT
		   (SELECT count(*) FROM node_relations),
		   (SELECT count(*) FROM events WHERE type='comment.created'),
		   (SELECT count(*) FROM attachments),
		   (SELECT count(*) FROM journey_projects),
		   (SELECT count(*) FROM aeon_search_nodes('zebra',NULL,NULL)),
		   (SELECT count(*) FROM events WHERE node_id IS NULL)`).Scan(
			&v.relations, &v.comments, &v.attachments, &v.journey, &v.search, &v.workspaceEvents)
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// ADR-003 P2: the RLS matrix. A principal sees a project, and everything in
// it, only through a workspace role holding nodes.read or a binding on that
// project; workspace-level nodes and workspace activity only through the
// workspace role.
func TestProjectVisibilityMatrix(t *testing.T) {
	w := newMatrixWorld(t)
	everything := []string{"GA-1", "GB-1", "ORG-1", "PA-1", "PB-1", "TA-1", "TB-1"}
	onlyA := []string{"GA-1", "PA-1", "TA-1"}
	type want struct {
		nodes, knowledge                                  []string
		relations, comments, attachments, journey, search int
		workspaceEvents                                   int
	}
	all := want{everything, []string{"GA-1", "GB-1"}, 2, 2, 2, 2, 7, 2 + w.bootstrapEvents}
	projectA := want{onlyA, []string{"GA-1"}, 1, 1, 1, 1, 3, 0}
	nothing := want{nil, nil, 0, 0, 0, 0, 0, 0}
	guestA := projectA
	guestA.workspaceEvents = 1 // its own profile edit
	cases := map[string]want{
		"owner": all, "admin": all, "member": all, "viewer": all,
		"guest":                         guestA,
		"deactivated guest":             nothing,
		"customer":                      nothing,
		"agent, workspace key":          all,
		"agent, project-scoped":         projectA,
		"agent, key from project guest": projectA,
		"agent, no binding":             nothing,
	}
	for name, expected := range cases {
		p, ok := w.people[name]
		if !ok {
			t.Fatalf("no principal %s", name)
		}
		got := w.view(t, tenant.WithPrincipal(t.Context(), p))
		if !slices.Equal(got.nodes, expected.nodes) || !slices.Equal(got.knowledge, expected.knowledge) ||
			got.relations != expected.relations || got.comments != expected.comments || got.attachments != expected.attachments ||
			got.journey != expected.journey || got.search != expected.search || got.workspaceEvents != expected.workspaceEvents {
			t.Errorf("%s (visibility %q):\n got  %+v\n want %+v", name, got.visibility, got, expected)
		}
	}
	// A principal from another tenant never borrows this tenant's bindings.
	foreign := w.people["owner"]
	foreign.TenantID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if got := w.view(t, tenant.WithPrincipal(t.Context(), foreign)); len(got.nodes) != 0 {
		t.Errorf("foreign-tenant principal sees %v", got.nodes)
	}
	// Losing the binding closes the project within the next transaction.
	guest := w.people["guest"]
	if _, err := w.d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, guest.ID); err != nil {
		t.Fatal(err)
	}
	if got := w.view(t, tenant.WithPrincipal(t.Context(), guest)); len(got.nodes) != 0 || got.comments != 0 {
		t.Errorf("guest keeps access after removal: %+v", got)
	}
}

// Built-in roles that open every project in SQL must be exactly the Go
// registry's workspace roles holding nodes.read, minus the project-only Guest.
func TestSQLVisibilityRolesMatchRegistry(t *testing.T) {
	d := dbtest.Open(t)
	ctx := dbtest.Seed(t.Context())
	var tid string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('p2-roles','P2 roles') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	for _, key := range builtinKeys {
		perms, _ := BuiltinPermissions(key)
		reads := contains(perms, "nodes.read")
		var workspace, project bool
		err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT aeon_role_reads_nodes($1,id,'workspace'),aeon_role_reads_nodes($1,id,'project') FROM roles WHERE tenant_id=$1 AND key=$2`, tid, key).Scan(&workspace, &project)
		})
		if err != nil {
			t.Fatal(err)
		}
		if workspace != (reads && key != "guest") || project != reads {
			t.Errorf("%s: SQL workspace=%v project=%v, registry nodes.read=%v", key, workspace, project, reads)
		}
	}
}
