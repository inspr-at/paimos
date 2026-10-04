// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/releases"
	"github.com/inspr-at/paimos/internal/tenant"
)

type visibilityFixture struct {
	d                         *dbtest.DB
	tenant                    string
	actor                     string
	projectA, projectB        string
	ticketA, ticketB, orgNode string
	kinds                     map[string]string
}

func newVisibilityFixture(t *testing.T, d *dbtest.DB, slug string) *visibilityFixture {
	t.Helper()
	ctx := dbtest.Seed(t.Context())
	f := &visibilityFixture{d: d, kinds: map[string]string{}}
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES($1,$1) RETURNING id::text`, slug).Scan(&f.tenant); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(ctx, d.App, f.tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,'organisation','Organisation','ORG','organisation')`, f.tenant); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT slug,id::text FROM node_kinds`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var slug, id string
			if err := rows.Scan(&slug, &id); err != nil {
				rows.Close()
				return err
			}
			f.kinds[slug] = id
		}
		rows.Close()
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Actor') RETURNING id::text`, f.tenant).Scan(&f.actor); err != nil {
			return err
		}
		f.projectA = insertNode(ctx, t, tx, f, "project", "PA-1", nil)
		f.projectB = insertNode(ctx, t, tx, f, "project", "PB-1", nil)
		f.ticketA = insertNode(ctx, t, tx, f, "ticket", "TA-1", &f.projectA)
		f.ticketB = insertNode(ctx, t, tx, f, "ticket", "TB-1", &f.projectB)
		f.orgNode = insertNode(ctx, t, tx, f, "organisation", "ORG-1", nil)
		if _, err := tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'blocks')`, f.tenant, f.ticketA, f.ticketB); err != nil {
			return err
		}
		for _, node := range []string{f.ticketA, f.ticketB} {
			if _, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after) VALUES($1,$2,$3,'comment.created','{"body_markdown":"x"}')`, f.tenant, f.actor, node); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO attachments(tenant_id,node_id,sha256,name,content_type,size,created_by) VALUES($1,$2,repeat('a',64),'a.txt','text/plain',1,$3)`, f.tenant, node, f.actor); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,type,after) VALUES($1,$2,'kind.created','{"x":1}')`, f.tenant, f.actor)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func insertNode(ctx context.Context, t *testing.T, tx pgx.Tx, f *visibilityFixture, kind, key string, parent *string) string {
	t.Helper()
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) VALUES($1,$2,$3,$3,$4) RETURNING id::text`, f.tenant, f.kinds[kind], key, parent).Scan(&id); err != nil {
		t.Fatalf("insert %s: %v", key, err)
	}
	return id
}

type visibleCounts struct {
	nodes, relations, nodeEvents, workspaceEvents, attachments, embeddingJobs int
}

func countVisible(t *testing.T, ctx context.Context, f *visibilityFixture) visibleCounts {
	t.Helper()
	var c visibleCounts
	err := db.InTenant(ctx, f.d.App, f.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
		  (SELECT count(*) FROM nodes),(SELECT count(*) FROM node_relations),
		  (SELECT count(*) FROM events WHERE node_id IS NOT NULL),(SELECT count(*) FROM events WHERE node_id IS NULL),
		  (SELECT count(*) FROM attachments),(SELECT count(*) FROM node_embedding_jobs)`).Scan(
			&c.nodes, &c.relations, &c.nodeEvents, &c.workspaceEvents, &c.attachments, &c.embeddingJobs)
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// ADR-003 P2: with visibility unset, nothing project-scoped is visible; an
// explicit service visibility opens all projects or exactly the listed ones.
func TestProjectVisibilityFailsClosed(t *testing.T) {
	d := dbtest.Open(t)
	f := newVisibilityFixture(t, d, "p2-closed")
	ctx := t.Context()

	none := countVisible(t, ctx, f)
	if none.nodes != 0 || none.relations != 0 || none.nodeEvents != 0 || none.attachments != 0 || none.embeddingJobs != 0 {
		t.Fatalf("unset visibility shows project data: %+v", none)
	}
	// Without a principal or a service visibility, not even workspace
	// activity is visible; an explicit service path (sign-in, bootstrap)
	// reads workspace events that name no project, and still no project data.
	if none.workspaceEvents != 0 {
		t.Fatalf("unset visibility shows workspace events: %+v", none)
	}
	system := countVisible(t, db.NoProjects(ctx, "test"), f)
	if system.workspaceEvents == 0 || system.nodes != 0 || system.nodeEvents != 0 || system.relations != 0 {
		t.Fatalf("NoProjects: %+v", system)
	}
	all := countVisible(t, db.AllProjects(ctx, "test"), f)
	if all.nodes != 5 || all.relations != 1 || all.nodeEvents != 2 || all.attachments != 2 || all.embeddingJobs != 5 {
		t.Fatalf("all projects: %+v", all)
	}
	onlyA := countVisible(t, db.OnlyProjects(ctx, f.projectA), f)
	// Project A and its ticket; not B, not the workspace-level organisation;
	// the relation to B is hidden because one end is invisible.
	if onlyA.nodes != 2 || onlyA.relations != 0 || onlyA.nodeEvents != 1 || onlyA.attachments != 1 || onlyA.embeddingJobs != 2 {
		t.Fatalf("only project A: %+v", onlyA)
	}
	if got := countVisible(t, db.OnlyProjects(ctx), f); got.nodes != 0 || got.nodeEvents != 0 {
		t.Fatalf("empty project list: %+v", got)
	}

	// Writes fail closed too: nothing can be created or changed unseen.
	err := db.InTenant(ctx, d.App, f.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) VALUES($1,$2,'TA-2','x',$3)`, f.tenant, f.kinds["ticket"], f.projectA)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "42501") && !strings.Contains(err.Error(), "parent node does not exist") {
		t.Fatalf("insert without visibility: %v", err)
	}
	var updated int64
	err = db.InTenant(db.OnlyProjects(ctx, f.projectA), d.App, f.tenant, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE nodes SET title='changed' WHERE id=$1`, f.ticketB)
		updated = tag.RowsAffected()
		return err
	})
	if err != nil || updated != 0 {
		t.Fatalf("update of an invisible node: rows=%d err=%v", updated, err)
	}
	err = db.InTenant(db.OnlyProjects(ctx, f.projectA), d.App, f.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE nodes SET parent_id=$1 WHERE id=$2`, f.projectB, f.ticketA)
		return err
	})
	if err == nil {
		t.Fatal("a caller moved a node into a project it cannot see")
	}
	err = db.InTenant(db.OnlyProjects(ctx, f.projectA), d.App, f.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'cites')`, f.tenant, f.ticketA, f.ticketB)
		return err
	})
	if err == nil {
		t.Fatal("a caller linked to a node it cannot see")
	}
	// A malformed setting is an error, never a wider view.
	err = db.InTenant(ctx, d.App, f.tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','garbage',true)`); err != nil {
			return err
		}
		var n int
		return tx.QueryRow(ctx, `SELECT count(*) FROM nodes`).Scan(&n)
	})
	if err == nil {
		t.Fatal("malformed visibility was accepted")
	}
}

// The project root follows every insert, move, re-parent and kind change, down
// the whole subtree, and cannot be written directly.
func TestNodeProjectRootFollowsMoves(t *testing.T) {
	d := dbtest.Open(t)
	f := newVisibilityFixture(t, d, "p2-moves")
	ctx := dbtest.Seed(t.Context())
	projectOf := func(tx pgx.Tx, id string) string {
		t.Helper()
		var project *string
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE id=$1`, id).Scan(&project); err != nil {
			t.Fatal(err)
		}
		if project == nil {
			return ""
		}
		return *project
	}
	err := db.InTenant(ctx, d.App, f.tenant, func(tx pgx.Tx) error {
		epic := insertNode(ctx, t, tx, f, "epic", "EA-1", &f.projectA)
		ticket := insertNode(ctx, t, tx, f, "ticket", "TA-9", &epic)
		task := insertNode(ctx, t, tx, f, "task", "KA-9", &ticket)
		deleted := insertNode(ctx, t, tx, f, "task", "KA-10", &ticket)
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=now() WHERE id=$1`, deleted); err != nil {
			return err
		}
		for _, id := range []string{f.projectA, epic, ticket, task, deleted} {
			if got := projectOf(tx, id); got != f.projectA {
				t.Fatalf("insert: %s has project %q, want A", id, got)
			}
		}
		if got := projectOf(tx, f.orgNode); got != "" {
			t.Fatalf("workspace node has project %q", got)
		}
		// Move the epic, with its whole subtree, to project B.
		if _, err := tx.Exec(ctx, `UPDATE nodes SET parent_id=$1 WHERE id=$2`, f.projectB, epic); err != nil {
			return err
		}
		for _, id := range []string{epic, ticket, task, deleted} {
			if got := projectOf(tx, id); got != f.projectB {
				t.Fatalf("move to B: %s has project %q", id, got)
			}
		}
		// Re-parent a subtree out of every project.
		if _, err := tx.Exec(ctx, `UPDATE nodes SET parent_id=NULL WHERE id=$1`, ticket); err != nil {
			return err
		}
		for _, id := range []string{ticket, task, deleted} {
			if got := projectOf(tx, id); got != "" {
				t.Fatalf("move to root: %s has project %q", id, got)
			}
		}
		if got := projectOf(tx, epic); got != f.projectB {
			t.Fatalf("epic lost its project: %q", got)
		}
		// Back under the epic, and a direct write of the column is ignored.
		if _, err := tx.Exec(ctx, `UPDATE nodes SET parent_id=$1 WHERE id=$2`, epic, ticket); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET project_id=$1 WHERE id=$2`, f.projectA, task); err != nil {
			return err
		}
		if got := projectOf(tx, task); got != f.projectB {
			t.Fatalf("direct write took effect: %q", got)
		}
		// A nested project is its own project; moving its parent leaves it.
		nested := insertNode(ctx, t, tx, f, "project", "PN-1", &epic)
		inner := insertNode(ctx, t, tx, f, "ticket", "TN-1", &nested)
		if projectOf(tx, nested) != nested || projectOf(tx, inner) != nested {
			t.Fatal("nested project does not own its subtree")
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET parent_id=$1 WHERE id=$2`, f.projectA, epic); err != nil {
			return err
		}
		if projectOf(tx, epic) != f.projectA || projectOf(tx, task) != f.projectA || projectOf(tx, nested) != nested || projectOf(tx, inner) != nested {
			t.Fatal("move crossed into a nested project")
		}
		// A kind change into a project takes the subtree along; back out, it
		// returns the subtree to the enclosing project.
		if _, err := tx.Exec(ctx, `UPDATE nodes SET kind_id=$1 WHERE id=$2`, f.kinds["project"], ticket); err != nil {
			return err
		}
		if projectOf(tx, ticket) != ticket || projectOf(tx, task) != ticket {
			t.Fatal("kind change to project did not cascade")
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET kind_id=$1 WHERE id=$2`, f.kinds["ticket"], ticket); err != nil {
			return err
		}
		if projectOf(tx, ticket) != f.projectA || projectOf(tx, task) != f.projectA {
			t.Fatal("kind change from project did not cascade")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A caller seeing only A loses the moved subtree at once.
	var visible int
	if err := db.InTenant(db.OnlyProjects(t.Context(), f.projectB), d.App, f.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE key IN ('EA-1','TA-9','KA-9')`).Scan(&visible)
	}); err != nil {
		t.Fatal(err)
	}
	if visible != 0 {
		t.Fatalf("project B still sees %d moved nodes", visible)
	}
}

// Production applies migrations as the table-owning NOSUPERUSER NOBYPASSRLS
// role, so FORCE ROW LEVEL SECURITY applies to every data step (AEON-158).
// Re-run the P2 data steps as that role on data from two tenants.
func TestProjectAccessMigrationsRunUnderForcedRLS(t *testing.T) {
	d := dbtest.Open(t)
	one := newVisibilityFixture(t, d, "p2-mig-one")
	two := newVisibilityFixture(t, d, "p2-mig-two")
	ctx := t.Context()
	// Undo what the superuser migration run did: no project roots, and a
	// classic external person with the P1 workspace Guest placeholder.
	if _, err := d.Admin.Exec(ctx, `ALTER TABLE nodes DISABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `UPDATE nodes SET project_id=NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `ALTER TABLE nodes ENABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	var external string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','External',ARRAY['external']) RETURNING id::text`, two.tenant).Scan(&external); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `ALTER TABLE role_bindings DISABLE TRIGGER role_bindings_guest_project_only`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE tenant_id=$1 AND key='guest'`, two.tenant, external); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `ALTER TABLE role_bindings ENABLE TRIGGER role_bindings_guest_project_only`); err != nil {
		t.Fatal(err)
	}
	// An event naming both tickets, with its recorded references cleared.
	if _, err := d.Admin.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after) VALUES($1,$2,$3::uuid,'relation.created',jsonb_build_object('source_node_id',$3::text,'target_node_id',$4::text))`, one.tenant, one.actor, one.ticketA, one.ticketB); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{`ALTER TABLE events DISABLE TRIGGER events_no_update_delete`, `UPDATE events SET node_refs='{}'`, `ALTER TABLE events ENABLE TRIGGER events_no_update_delete`} {
		if _, err := d.Admin.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"0820_node_project_root.sql", "0822_guest_project_only.sql", "0823_event_reference_visibility.sql"} {
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		start := strings.Index(text, "DO $$")
		end := strings.Index(text[start:], "\n$$;") + start + len("\n$$;")
		if start < 0 || end <= start {
			t.Fatalf("%s: no data step", name)
		}
		if _, err := d.App.Exec(ctx, text[start:end]); err != nil {
			t.Fatalf("%s data step as the app role: %v", name, err)
		}
	}
	for _, f := range []*visibilityFixture{one, two} {
		var missing, wrong int
		if err := d.Admin.QueryRow(ctx, `SELECT
		   count(*) FILTER (WHERE project_id IS NULL AND id<>$2),
		   count(*) FILTER (WHERE id IN ($3,$4) AND project_id IS DISTINCT FROM (CASE WHEN id=$3 THEN $5 ELSE $6 END)::uuid)
		   FROM nodes WHERE tenant_id=$1`, f.tenant, f.orgNode, f.ticketA, f.ticketB, f.projectA, f.projectB).Scan(&missing, &wrong); err != nil {
			t.Fatal(err)
		}
		if missing != 0 || wrong != 0 {
			t.Fatalf("tenant %s backfill: %d missing, %d wrong", f.tenant, missing, wrong)
		}
	}
	var refs []string
	if err := d.Admin.QueryRow(ctx, `SELECT array(SELECT unnest(node_refs)::text ORDER BY 1) FROM events WHERE type='relation.created' AND tenant_id=$1`, one.tenant).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if want := []string{min(one.ticketA, one.ticketB), max(one.ticketA, one.ticketB)}; len(refs) != 2 || refs[0] != want[0] || refs[1] != want[1] {
		t.Fatalf("backfilled references %v, want %v", refs, want)
	}
	var guests, events int
	if err := d.Admin.QueryRow(ctx, `SELECT
	   (SELECT count(*) FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id WHERE b.scope_type='workspace' AND r.key='guest'),
	   (SELECT count(*) FROM events WHERE type='binding.removed' AND before->>'principal_id'=$1)`, external).Scan(&guests, &events); err != nil {
		t.Fatal(err)
	}
	if guests != 0 || events != 1 {
		t.Fatalf("workspace guest bindings %d, removal events %d", guests, events)
	}
	// And the placeholder cannot come back.
	err := db.InTenant(dbtest.Seed(ctx), d.App, two.tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT aeon_bind_legacy_principal($1::uuid,$2::uuid)`, two.tenant, external); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM role_bindings WHERE principal_id=$1`, external).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("external person rebound: %d", n)
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE tenant_id=$1 AND key='guest'`, two.tenant, external)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "guest is a project role") {
		t.Fatalf("workspace guest binding: %v", err)
	}
}

// Review finding 2: an event about a visible node is hidden from a caller who
// does not see every node it names (former parent, former project, journey
// references, relation ends, classic relation targets, ID lists); a value
// that is no node ID hides it too. A caller who sees both projects reads all.
func TestEventReferencesFollowVisibility(t *testing.T) {
	d := dbtest.Open(t)
	f := newVisibilityFixture(t, d, "p2-event-refs")
	ctx := t.Context()
	a, b := f.projectA, f.projectB
	events := []struct {
		name, typ, before, after string
		hiddenFromA              bool
	}{
		{"moved from B", "node.moved", `{"id":"` + f.ticketA + `","parent_id":"` + b + `"}`, `{"id":"` + f.ticketA + `","parent_id":"` + a + `"}`, true},
		{"moved within A", "node.moved", `{"id":"` + f.ticketA + `","parent_id":"` + a + `"}`, `{"id":"` + f.ticketA + `","parent_id":"` + a + `"}`, false},
		{"project move from B", "node.project_moved", `{"node":{"parent_id":"` + b + `","key":"PB-9"},"journey":{"project_id":"` + b + `","feature_id":null,"release_id":null}}`, `{"node":{"parent_id":"` + a + `"},"journey":{"project_id":"` + a + `"}}`, true},
		{"classic parent change", "import.parent_changed", `{"parent_id":"` + a + `","project_id":"` + b + `"}`, `{"parent_id":"` + a + `","project_id":"` + a + `"}`, true},
		{"relation to B", "relation.created", ``, `{"source_node_id":"` + f.ticketA + `","target_node_id":"` + f.ticketB + `"}`, true},
		{"classic relation to B", "import.relation", ``, `{"record":{"target_key":"TB-1","target_title":"secret"}}`, true},
		{"classic relation within A", "import.relation", ``, `{"record":{"target_key":"PA-1"}}`, false},
		{"classic relation to nowhere", "import.relation", ``, `{"record":{"target_key":"NONE-1"}}`, true},
		{"plan naming B", "journey.release_planned", ``, `{"ordered_ticket_ids":["` + f.ticketA + `","` + f.ticketB + `"]}`, true},
		{"plan within A", "journey.release_planned", ``, `{"ordered_ticket_ids":["` + f.ticketA + `"]}`, false},
		{"malformed reference", "node.updated", ``, `{"parent_id":"not-a-node"}`, true},
		{"numeric reference", "node.updated", ``, `{"parent_id":7}`, true},
		{"classic record numbers", "import.history", ``, `{"record":{"issue_id":7,"project_id":9}}`, false},
		{"free-form fields", "node.updated", ``, `{"parent_id":"` + a + `","fields":{"release":{"id":3}}}`, false},
		{"plain comment", "comment.created", ``, `{"body_markdown":"hi"}`, false},
	}
	err := db.InTenant(dbtest.Seed(ctx), d.App, f.tenant, func(tx pgx.Tx) error {
		for _, e := range events {
			var before any
			if e.before != "" {
				before = e.before
			}
			if _, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,before,after) VALUES($1,$2,$3,$4,$5::jsonb,$6::jsonb)`,
				f.tenant, f.actor, f.ticketA, e.typ, before, e.after); err != nil {
				return fmt.Errorf("%s: %w", e.name, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	visible := func(ctx context.Context) map[string]bool {
		out := map[string]bool{}
		err := db.InTenant(ctx, d.App, f.tenant, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT type, coalesce(before::text,''), after::text FROM events WHERE node_id=$1`, f.ticketA)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var typ, before, after string
				if err := rows.Scan(&typ, &before, &after); err != nil {
					return err
				}
				for _, e := range events {
					if e.typ == typ && jsonEqual(t, e.after, after) && (e.before == "" && before == "" || e.before != "" && before != "" && jsonEqual(t, e.before, before)) {
						out[e.name] = true
					}
				}
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	onlyA := visible(db.OnlyProjects(ctx, a))
	both := visible(db.OnlyProjects(ctx, a, b))
	all := visible(db.AllProjects(ctx, "test"))
	for _, e := range events {
		if onlyA[e.name] == e.hiddenFromA {
			t.Errorf("%s: visible to a caller of A only = %v", e.name, onlyA[e.name])
		}
		if !all[e.name] {
			t.Errorf("%s: hidden from every-project visibility", e.name)
		}
		wantBoth := !strings.Contains(e.name, "malformed") && !strings.Contains(e.name, "numeric") && !strings.Contains(e.name, "nowhere")
		if both[e.name] != wantBoth {
			t.Errorf("%s: visible to a caller of A and B = %v, want %v", e.name, both[e.name], wantBoth)
		}
	}
}

func jsonEqual(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// Review round 2, findings 1 and 2: references inside arrays count (a release
// plan names its tickets, features and screens in tickets[] and features[]),
// a batch names its items, and neither an event without a node nor the
// caller's own event skips the check. A transaction with no principal and no
// service visibility reads no event at all.
func TestNestedAndNodelessEventReferences(t *testing.T) {
	d := dbtest.Open(t)
	f := newVisibilityFixture(t, d, "p2-nested-refs")
	ctx := t.Context()
	// The actor is a guest of project A only.
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, f.tenant, f.actor, f.projectA); err != nil {
		t.Fatal(err)
	}
	plan := func(ticket, screen string) string {
		w := releases.Walker{ReleaseID: f.ticketA, ProjectID: f.projectA, State: "planning", Revision: 2,
			Features: []releases.Feature{{NodeID: f.ticketA, Key: "TA-1", Title: "Feature"}},
			Tickets:  []releases.Ticket{{NodeID: ticket, Key: "K-1", Title: "secret title", ScreenIDs: []string{screen}}}}
		b, err := json.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	batch := func(ids ...string) string {
		items := []map[string]any{}
		for _, id := range ids {
			items = append(items, map[string]any{"id": id, "key": "K-" + id[:4], "parent_id": nil, "fields": map[string]any{"x": 1}})
		}
		b, err := json.Marshal(map[string]any{"items": items})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cases := []struct {
		name, typ  string
		node       *string
		after      string
		visibleToA bool
	}{
		{"plan within A", "journey.release_planned", &f.ticketA, plan(f.ticketA, f.ticketA), true},
		{"plan with a ticket of B", "journey.release_planned", &f.ticketA, plan(f.ticketB, f.ticketA), false},
		{"plan with a screen of B", "journey.release_planned", &f.ticketA, plan(f.ticketA, f.ticketB), false},
		{"batch within A", "node.bulk_changed", nil, batch(f.ticketA), true},
		{"batch touching B", "node.bulk_changed", nil, batch(f.ticketA, f.ticketB), false},
		{"own workspace activity", "profile.updated", nil, `{"principal_id":"` + f.actor + `"}`, true},
		{"own harness event naming B", "harness.heartbeat", &f.ticketA, `{"project_id":"` + f.projectB + `"}`, false},
		{"deeply nested", "node.updated", &f.ticketA, strings.Repeat(`{"a":`, 20) + `{"parent_id":"` + f.ticketA + `"}` + strings.Repeat(`}`, 20), false},
	}
	err := db.InTenant(dbtest.Seed(ctx), d.App, f.tenant, func(tx pgx.Tx) error {
		for _, c := range cases {
			if _, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after) VALUES($1,$2,$3,$4,$5::jsonb)`, f.tenant, f.actor, c.node, c.typ, c.after); err != nil {
				return fmt.Errorf("%s: %w", c.name, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	visible := func(ctx context.Context) map[string]bool {
		out := map[string]bool{}
		err := db.InTenant(ctx, d.App, f.tenant, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT type, after::text FROM events WHERE actor_principal_id=$1`, f.actor)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var typ, after string
				if err := rows.Scan(&typ, &after); err != nil {
					return err
				}
				for _, c := range cases {
					if c.typ == typ && jsonEqual(t, c.after, after) {
						out[c.name] = true
					}
				}
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	actor := tenant.Principal{ID: f.actor, TenantID: f.tenant, Kind: tenant.Person}
	asGuest := visible(tenant.WithPrincipal(ctx, actor))
	all := visible(db.AllProjects(ctx, "test"))
	unset := visible(ctx)
	for _, c := range cases {
		if asGuest[c.name] != c.visibleToA {
			t.Errorf("%s: visible to its actor, a guest of A = %v, want %v", c.name, asGuest[c.name], c.visibleToA)
		}
		if !all[c.name] {
			t.Errorf("%s: hidden from every-project visibility", c.name)
		}
		if unset[c.name] {
			t.Errorf("%s: visible without a principal or service visibility", c.name)
		}
	}
}

// A node reference is any UUID that is a node of this tenant, under any key,
// including inside fields. Principal, role and binding ids are not nodes.
// Journey release ids, a group's project_ids and a reassigned binding's
// scope_id are the shapes key-name matching left visible to a guest of A.
func TestEventReferencesByValue(t *testing.T) {
	d := dbtest.Open(t)
	f := newVisibilityFixture(t, d, "p2-value-refs")
	ctx := t.Context()
	var relA, relB, relB2, gone string
	err := db.InTenant(dbtest.Seed(ctx), d.App, f.tenant, func(tx pgx.Tx) error {
		relA = insertNode(ctx, t, tx, f, "ticket", "RA-1", &f.projectA)
		relB = insertNode(ctx, t, tx, f, "ticket", "RB-1", &f.projectB)
		relB2 = insertNode(ctx, t, tx, f, "ticket", "RB-2", &f.projectB)
		gone = insertNode(ctx, t, tx, f, "ticket", "DB-1", &f.projectB)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `UPDATE nodes SET deleted_at=now() WHERE tenant_id=$1 AND id=$2`, f.tenant, gone); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, f.tenant, f.actor, f.projectA); err != nil {
		t.Fatal(err)
	}
	const (
		stranger = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		roleID   = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		groupID  = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	)
	journey := func(marker, current string, superseded []string) map[string]any {
		return map[string]any{
			"marker": marker, "project_node_id": f.projectA, "profile": "delivery", "revision": 4,
			"decision": "go", "brief_confirmed": true, "requirements_revision": 2, "agreed_requirements_revision": 2,
			"current_release_id": current, "release_state": "planned", "stage": "build", "action": "decide",
			"approval_request_id": stranger, "superseded_release_ids": superseded,
		}
	}
	group := func(marker string, projects []string) map[string]any {
		return map[string]any{
			"marker": marker, "id": groupID, "name": "Clients", "position": 1, "project_ids": projects,
			"created_by": f.actor, "created_at": "2026-09-26T00:00:00Z", "updated_at": "2026-09-26T00:00:00Z",
		}
	}
	binding := func(marker, scopeType string, scope any) map[string]any {
		return map[string]any{
			"marker": marker, "principal_id": f.actor, "role_id": roleID, "binding_id": groupID,
			"scope_type": scopeType, "scope_id": scope,
		}
	}
	type marked struct {
		marker     string
		typ        string
		node       *string
		before     any
		after      any
		visible    bool
		wantRefs   []string
		absentRefs []string
	}
	cases := []marked{
		{"journey-decided-b", "journey.decided", &f.projectA, nil, journey("journey-decided-b", relB, []string{relB2}), false, []string{f.projectA, relB, relB2}, []string{stranger, f.actor}},
		{"journey-opened-b", "journey.release_opened", &f.projectA, nil, journey("journey-opened-b", relB, []string{relB, relB2}), false, []string{relB, relB2}, []string{stranger}},
		{"journey-planned-b", "journey.release_planned", &f.projectA, nil, journey("journey-planned-b", relB, []string{gone}), false, []string{relB, gone}, []string{stranger}},
		{"journey-decided-a", "journey.decided", &f.projectA, nil, journey("journey-decided-a", relA, []string{relA}), true, []string{f.projectA, relA}, []string{stranger, f.actor}},
		{"group-created-b", "project_group.created", nil, nil, group("group-created-b", []string{f.projectA, f.projectB}), false, []string{f.projectA, f.projectB}, []string{f.actor, groupID}},
		{"group-created-a", "project_group.created", nil, nil, group("group-created-a", []string{f.projectA}), true, []string{f.projectA}, []string{f.actor, groupID}},
		{"group-deleted-b", "project_group.deleted", nil, group("group-deleted-b", []string{f.projectB}), nil, false, []string{f.projectB}, []string{f.actor, groupID}},
		{"binding-b", "authz.binding_reassigned", nil, nil, binding("binding-b", "project", f.projectB), false, []string{f.projectB}, []string{f.actor, roleID, groupID}},
		{"binding-a", "authz.binding_reassigned", nil, nil, binding("binding-a", "project", f.projectA), true, []string{f.projectA}, []string{f.actor, roleID, groupID}},
		{"binding-workspace", "authz.binding_reassigned", nil, nil, binding("binding-workspace", "workspace", nil), true, nil, []string{f.actor, roleID, groupID}},
		{"fields-b", "node.updated", &f.ticketA, nil, map[string]any{"marker": "fields-b", "fields": map[string]any{"custom_slot": relB, "classic": map[string]any{"project_id": 9}}}, false, []string{relB}, nil},
		{"fields-a", "node.updated", &f.ticketA, nil, map[string]any{"marker": "fields-a", "fields": map[string]any{"custom_slot": relA, "classic": map[string]any{"project_id": "9", "assignee_id": 7}, "assignee_id": f.actor}}, true, []string{relA}, []string{f.actor}},
		{"deleted-b", "node.updated", &f.ticketA, nil, map[string]any{"marker": "deleted-b", "slot": gone}, false, []string{gone}, nil},
	}
	rng := rand.New(rand.NewPCG(153, 4))
	plantKeys := []string{"current_release_id", "superseded_release_ids", "project_ids", "scope_id", "slot", "note_ref"}
	hidePool := []string{f.projectB, f.ticketB, relB, relB2, gone}
	showPool := []string{f.projectA, f.ticketA, relA}
	for i := 0; i < 24; i++ {
		key := plantKeys[rng.IntN(len(plantKeys))]
		hidden := i%2 == 0
		id := showPool[rng.IntN(len(showPool))]
		if hidden {
			id = hidePool[rng.IntN(len(hidePool))]
		}
		var planted any = id
		if strings.HasSuffix(key, "_ids") {
			planted = []any{id}
		}
		marker := fmt.Sprintf("prop-%02d", i)
		cases = append(cases, marked{
			marker: marker, typ: "node.updated", node: &f.ticketA, visible: !hidden,
			after: map[string]any{
				"marker": marker, "principal_id": f.actor, "role_id": roleID, "binding_id": groupID,
				"body": buryValue(rng, key, planted, rng.IntN(5)),
			},
			wantRefs:   []string{id},
			absentRefs: []string{f.actor, roleID, groupID},
		})
	}
	actor := tenant.Principal{ID: f.actor, TenantID: f.tenant, Kind: tenant.Person}
	err = db.InTenant(tenant.WithPrincipal(ctx, actor), d.App, f.tenant, func(tx pgx.Tx) error {
		for _, c := range cases {
			var before, after any
			if c.before != nil {
				raw, err := json.Marshal(c.before)
				if err != nil {
					return err
				}
				before = string(raw)
			}
			if c.after != nil {
				raw, err := json.Marshal(c.after)
				if err != nil {
					return err
				}
				after = string(raw)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,before,after) VALUES($1,$2,$3,$4,$5::jsonb,$6::jsonb)`, f.tenant, f.actor, c.node, c.typ, before, after); err != nil {
				return fmt.Errorf("%s: %w", c.marker, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := func(ctx context.Context) map[string]bool {
		out := map[string]bool{}
		err := db.InTenant(ctx, d.App, f.tenant, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT coalesce(after->>'marker', before->>'marker') FROM events WHERE actor_principal_id=$1 AND coalesce(after->>'marker', before->>'marker') <> ''`, f.actor)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var marker string
				if err := rows.Scan(&marker); err != nil {
					return err
				}
				out[marker] = true
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	asGuest := seen(tenant.WithPrincipal(ctx, actor))
	all := seen(db.AllProjects(ctx, "test"))
	unset := seen(ctx)
	for _, c := range cases {
		if asGuest[c.marker] != c.visible {
			t.Errorf("%s: visible to a guest of A = %v, want %v", c.marker, asGuest[c.marker], c.visible)
		}
		if !all[c.marker] {
			t.Errorf("%s: hidden from every-project visibility", c.marker)
		}
		if unset[c.marker] {
			t.Errorf("%s: visible without a principal or service visibility", c.marker)
		}
		var refs []string
		if err := d.Admin.QueryRow(ctx, `SELECT coalesce(array(SELECT unnest(node_refs)::text), '{}') FROM events WHERE tenant_id=$1 AND coalesce(after->>'marker', before->>'marker')=$2`, f.tenant, c.marker).Scan(&refs); err != nil {
			t.Fatal(err)
		}
		have := map[string]bool{}
		for _, id := range refs {
			have[id] = true
		}
		for _, id := range c.wantRefs {
			if !have[id] {
				t.Errorf("%s: node_refs %v lacks %s", c.marker, refs, id)
			}
		}
		for _, id := range c.absentRefs {
			if have[id] {
				t.Errorf("%s: node_refs %v includes non-node %s", c.marker, refs, id)
			}
		}
	}
}

// buryValue nests value under key through objects and arrays, sometimes inside
// fields. Keys are not the historical reference-key list.
func buryValue(rng *rand.Rand, key string, value any, depth int) any {
	if depth <= 0 {
		return map[string]any{key: value}
	}
	switch rng.IntN(4) {
	case 0:
		return map[string]any{"fields": buryValue(rng, key, value, depth-1)}
	case 1:
		return []any{buryValue(rng, key, value, depth-1), map[string]any{"n": rng.IntN(9)}}
	case 2:
		return map[string]any{"slot_" + fmt.Sprint(rng.IntN(10000)): buryValue(rng, key, value, depth-1)}
	default:
		return map[string]any{"wrap": buryValue(rng, key, value, depth-1), "note": "plain"}
	}
}

// A revocation can commit between initial visibility setup and the request's
// authorization fence. The final handler must see the post-fence project set.
func TestTenantGuardRefreshesVisibilityAfterRevocation(t *testing.T) {
	d := dbtest.Open(t)
	f := newVisibilityFixture(t, d, "guard-visibility")
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, f.tenant, f.actor, f.projectA); err != nil {
		t.Fatal(err)
	}
	ctx := tenant.WithPrincipal(t.Context(), tenant.Principal{ID: f.actor, TenantID: f.tenant, Kind: tenant.Person})
	if countVisible(t, ctx, f).nodes != 2 {
		t.Fatal("project grant fixture is not visible")
	}
	ctx = db.WithTenantGuard(ctx, func(ctx context.Context, tx pgx.Tx, tid string) error {
		// This is the deterministic interleaving point, after enterTenant and before
		// the fence is acquired. Commit the revocation on a separate connection.
		if _, err := d.Admin.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, tid, f.actor); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, tid)
		return err
	})
	if got := countVisible(t, ctx, f); got.nodes != 0 || got.attachments != 0 || got.nodeEvents != 0 {
		t.Fatalf("stale project access after guard: %+v", got)
	}
}
