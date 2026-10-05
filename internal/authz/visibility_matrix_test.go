// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

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
