// SPDX-License-Identifier: AGPL-3.0-only
package principallink

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

type fixture struct {
	d   *dbtest.DB
	s   *Service
	tid string
	t   *testing.T
}

func setup(t *testing.T) *fixture {
	d := dbtest.Open(t)
	tid, err := tenantbootstrap.Create(t.Context(), d.App, "links", "Links")
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{d: d, s: New(d.App), tid: tid, t: t}
}
func (f *fixture) tx(fn func(pgx.Tx) error) {
	f.t.Helper()
	if err := db.InTenant(dbtest.Seed(f.t.Context()), f.d.App, f.tid, fn); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) person(name, issuer, email string) string {
	f.t.Helper()
	var id string
	f.tx(func(tx pgx.Tx) error {
		var identity *string
		if issuer != "" {
			var v string
			if err := tx.QueryRow(f.t.Context(), `INSERT INTO identities(issuer,subject,email) VALUES($1,$2,$3) RETURNING id::text`, issuer, name, email).Scan(&v); err != nil {
				return err
			}
			identity = &v
		}
		return tx.QueryRow(f.t.Context(), `INSERT INTO principals(tenant_id,kind,name,identity_id) VALUES($1,'person',$2,$3) RETURNING id::text`, f.tid, name, identity).Scan(&id)
	})
	return id
}
func (f *fixture) count() int {
	var n int
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1`, f.tid).Scan(&n)
	})
	return n
}

func TestPrincipalLinkTenantBeforeAdvisory(t *testing.T) {
	for _, cli := range []bool{false, true} {
		t.Run(fmt.Sprintf("operator=%t", cli), func(t *testing.T) {
			f := setup(t)
			from := f.person("Alias", "", "")
			to := f.person("Canonical", "", "")
			dbtest.BindRole(t, f.d, f.tid, from, "member")
			dbtest.TenantBeforeAdvisory(t, f.d, f.tid, f.tid, 532, func(ctx context.Context) error {
				if cli {
					_, err := f.s.Link(ctx, "links", from, to)
					return err
				}
				return db.InTenant(dbtest.Seed(ctx), f.d.App, f.tid, func(tx pgx.Tx) error {
					_, err := LinkTx(ctx, tx, f.tid, from, to, to, "principal.linked", "principal.unlinked")
					return err
				})
			})
			var linked string
			var bindings int
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT linked_to::text,
				(SELECT count(*) FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2)
				FROM principals WHERE tenant_id=$1 AND id=$2`, f.tid, from).Scan(&linked, &bindings); err != nil {
				t.Fatal(err)
			}
			if linked != to || bindings != 0 {
				t.Fatalf("linked=%s want %s; alias bindings=%d", linked, to, bindings)
			}
		})
	}
}

func TestLinkUnlinkReplayAndSuggestions(t *testing.T) {
	f := setup(t)
	a := f.person("mba", "paimos-classic", "same@example.test")
	b := f.person("Markus Barta", "https://id.example.test", "SAME@example.test")
	f.person("same", "paimos-classic", "")
	f.person("unrelated", "paimos-classic", "")
	before := f.count()
	var out bytes.Buffer
	if err := Run(t.Context(), f.d.App, []string{"link", "--tenant", "links", "--suggest"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "same_email") || !strings.Contains(out.String(), "username_email_local_part") || strings.Contains(out.String(), "unrelated") {
		t.Fatal(out.String())
	}
	if f.count() != before {
		t.Fatal("suggest wrote events")
	}
	r, err := f.s.Link(t.Context(), "links", "mba", b)
	if err != nil || !r.Changed || r.Person.LinkedTo == nil || *r.Person.LinkedTo != b {
		t.Fatalf("link %+v %v", r, err)
	}
	n := f.count()
	if r, err = f.s.Link(t.Context(), "links", a, "Markus Barta"); err != nil || r.Changed || f.count() != n {
		t.Fatalf("replay %+v %v", r, err)
	}
	f.tx(func(tx pgx.Tx) error {
		id, name, err := Resolve(t.Context(), tx, f.tid, a)
		if err != nil {
			return err
		}
		if id != b || name != "Markus Barta" {
			return fmt.Errorf("resolution %s %s", id, name)
		}
		var typ, actor, previous string
		if err := tx.QueryRow(t.Context(), `SELECT e.type,p.name,e.before->>'name' FROM events e JOIN principals p ON p.id=e.actor_principal_id AND p.tenant_id=e.tenant_id WHERE e.tenant_id=$1 AND e.type='principal.linked'`, f.tid).Scan(&typ, &actor, &previous); err != nil {
			return err
		}
		if actor != "Principal link operator" || previous != "mba" {
			return fmt.Errorf("wrong audit actor/snapshot")
		}
		return nil
	})
	if r, err = f.s.Unlink(t.Context(), "links", "mba"); err != nil || !r.Changed || r.Person.LinkedTo != nil {
		t.Fatalf("unlink %+v %v", r, err)
	}
	n = f.count()
	if r, err = f.s.Unlink(t.Context(), "links", a); err != nil || r.Changed || f.count() != n {
		t.Fatalf("unlink replay %+v %v", r, err)
	}
	f.tx(func(tx pgx.Tx) error {
		id, name, err := Resolve(t.Context(), tx, f.tid, a)
		if err == nil && (id != a || name != "mba") {
			return fmt.Errorf("unlink resolution")
		}
		return err
	})
}

func TestSuggestionsRequireASCIIMailboxMatch(t *testing.T) {
	for _, tc := range []struct {
		name, classicEmail, realEmail, reason string
	}{
		{"unrelated", "admİn@example.test", "admin@example.test", ""},
		{"unrelated", "marK@example.test", "mark@example.test", ""},
		{"unrelated", "ſam@example.test", "sam@example.test", ""},
		{"unrelated", "MARK@example.test", "mark@example.test", "same_email"},
		{"unrelated", "ADMİN@example.test", "admİn@example.test", "same_email"},
		{"admİn", "", "admin@example.test", ""},
		{"marK", "", "mark@example.test", ""},
		{"MARK", "", "mark@example.test", "username_email_local_part"},
	} {
		t.Run(tc.name+"/"+tc.classicEmail+"/"+tc.realEmail, func(t *testing.T) {
			f := setup(t)
			from := f.person(tc.name, "paimos-classic", tc.classicEmail)
			to := f.person("Real person", "https://id.example.test", tc.realEmail)
			before := f.count()
			suggestions, err := f.s.Suggest(t.Context(), "links")
			if err != nil {
				t.Fatal(err)
			}
			if tc.reason == "" {
				if len(suggestions) != 0 {
					t.Fatalf("different Unicode mailbox suggested: %+v", suggestions)
				}
			} else if len(suggestions) != 1 || suggestions[0].From.ID != from || suggestions[0].To.ID != to || suggestions[0].Reason != tc.reason {
				t.Fatalf("matching suggestion missing: %+v", suggestions)
			}
			if f.count() != before {
				t.Fatal("suggestions changed events")
			}
		})
	}
}
func TestInvalidLinksAndDatabaseConstraints(t *testing.T) {
	f := setup(t)
	a := f.person("alias", "", "")
	b := f.person("real", "", "")
	c := f.person("third", "", "")
	foreign, err := tenantbootstrap.Create(t.Context(), f.d.App, "foreign", "Foreign")
	if err != nil {
		t.Fatal(err)
	}
	var outside, agent string
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, foreign, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','foreign') RETURNING id::text`, foreign).Scan(&outside)
	}); err != nil {
		t.Fatal(err)
	}
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','bot') RETURNING id::text`, f.tid).Scan(&agent)
	})
	for _, pair := range [][2]string{{a, a}, {a, outside}, {a, agent}, {agent, b}, {"missing", b}} {
		if _, err := f.s.Link(t.Context(), "links", pair[0], pair[1]); err == nil {
			t.Fatalf("accepted %v", pair)
		}
	}
	if _, err := f.s.Link(t.Context(), "links", a, b); err != nil {
		t.Fatal(err)
	}
	n := f.count()
	for _, pair := range [][2]string{{b, a}, {b, c}, {c, a}, {a, c}} {
		if _, err := f.s.Link(t.Context(), "links", pair[0], pair[1]); err == nil {
			t.Fatalf("accepted chain/relink %v", pair)
		}
	}
	if f.count() != n {
		t.Fatal("rejected changes wrote events")
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE principals SET linked_to=$2 WHERE id=$1`, []any{b, c}},
		{`UPDATE principals SET linked_to=$2 WHERE id=$1`, []any{c, a}},
		{`UPDATE principals SET linked_to=$2 WHERE id=$1`, []any{c, outside}},
		{`UPDATE principals SET linked_to=$2 WHERE id=$1`, []any{c, agent}},
		{`UPDATE principals SET linked_to=$2 WHERE id=$1`, []any{agent, c}},
		{`UPDATE principals SET kind='agent' WHERE id=$1`, []any{b}},
	} {
		if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.tid, func(tx pgx.Tx) error { _, err := tx.Exec(t.Context(), q.sql, q.args...); return err }); err == nil {
			t.Fatal("database accepted invalid topology")
		}
	}
	f.person("duplicate", "", "")
	f.person("duplicate", "", "")
	if _, err := f.s.Link(t.Context(), "links", "duplicate", c); err == nil {
		t.Fatal("ambiguous name accepted")
	}
}
func TestAtomicRollbackAndConcurrentLinks(t *testing.T) {
	f := setup(t)
	a := f.person("a", "", "")
	b := f.person("b", "", "")
	c := f.person("c", "", "")
	// Force event insertion failure after the link update; the update must roll back.
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.Admin, f.tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE FUNCTION reject_link_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='principal.linked' THEN RAISE EXCEPTION 'test failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_link_event BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_link_event()`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Link(t.Context(), "links", a, b); err == nil {
		t.Fatal("expected event failure")
	}
	f.tx(func(tx pgx.Tx) error {
		var link *string
		if err := tx.QueryRow(t.Context(), `SELECT linked_to::text FROM principals WHERE id=$1`, a).Scan(&link); err != nil {
			return err
		}
		if link != nil {
			return fmt.Errorf("link escaped rollback")
		}
		_, err := tx.Exec(t.Context(), `DROP TRIGGER reject_link_event ON events`)
		return err
	})
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, pair := range [][2]string{{a, b}, {b, c}} {
		wg.Add(1)
		go func(pair [2]string) {
			defer wg.Done()
			<-start
			_, err := f.s.Link(context.Background(), "links", pair[0], pair[1])
			results <- err
		}(pair)
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("concurrent chain successes %d", success)
	}
}

func TestLinkAuditsEveryBindingRemovalAndRollsBackOnAuditFailure(t *testing.T) {
	f := setup(t)
	a := f.person("classic admin", "paimos-classic", "classic@example.test")
	b := f.person("signed in", "https://id.example.test", "real@example.test")
	var project string
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE principals SET roles=ARRAY['admin'] WHERE tenant_id=$1 AND id=$2`, f.tid, a); err != nil {
			return err
		}
		if err := dbtest.BindLegacyTx(t.Context(), tx, f.tid, a); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state)
			SELECT $1::uuid,id,'AUD-1','Audit project','active' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, f.tid).Scan(&project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
			SELECT $1::uuid,$2::uuid,id,'project',$3::uuid FROM roles WHERE tenant_id=$1 AND key='guest'`, f.tid, a, project)
		return err
	})
	// Reject the removal event after DELETE: both bindings and the link must
	// roll back with it, with no partially written removal audit.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE FUNCTION reject_binding_removal() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN IF NEW.type='binding.removed' THEN RAISE EXCEPTION 'test audit failure'; END IF; RETURN NEW; END $$;
			CREATE TRIGGER reject_binding_removal BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_binding_removal()`)
		return err
	})
	if _, err := f.s.Link(t.Context(), "links", a, b); err == nil {
		t.Fatal("link committed despite removal audit failure")
	}
	f.tx(func(tx pgx.Tx) error {
		var link *string
		var bindings, removed int
		if err := tx.QueryRow(t.Context(), `SELECT linked_to::text FROM principals WHERE tenant_id=$1 AND id=$2`, f.tid, a).Scan(&link); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT
			(SELECT count(*) FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2),
			(SELECT count(*) FROM events WHERE tenant_id=$1 AND type='binding.removed')`, f.tid, a).Scan(&bindings, &removed); err != nil {
			return err
		}
		if link != nil || bindings != 2 || removed != 0 {
			return fmt.Errorf("audit failure escaped rollback: link=%v bindings=%d removals=%d", link, bindings, removed)
		}
		_, err := tx.Exec(t.Context(), `DROP TRIGGER reject_binding_removal ON events`)
		return err
	})
	if _, err := f.s.Link(t.Context(), "links", a, b); err != nil {
		t.Fatal(err)
	}
	f.tx(func(tx pgx.Tx) error {
		var workspace, projectRemoved int
		if err := tx.QueryRow(t.Context(), `SELECT
			count(*) FILTER (WHERE node_id IS NULL AND before->>'scope_type'='workspace' AND before->'role'->>'key'='admin'),
			count(*) FILTER (WHERE node_id IS NULL AND $3::uuid=ANY(node_refs) AND before->>'project_id'=$3::text AND before->>'scope_type'='project' AND before->'role'->>'key'='guest')
			FROM events WHERE tenant_id=$1 AND type='binding.removed' AND before->>'principal_id'=$2
			  AND before->>'id' IS NOT NULL AND after IS NULL
			  AND actor_principal_id=(SELECT id FROM principals WHERE tenant_id=$1 AND name='Principal link operator')`, f.tid, a, project).Scan(&workspace, &projectRemoved); err != nil {
			return err
		}
		if workspace != 1 || projectRemoved != 1 {
			return fmt.Errorf("missing deleted binding audit: workspace=%d project=%d", workspace, projectRemoved)
		}
		return nil
	})
	beforeReplay := f.count()
	if result, err := f.s.Link(t.Context(), "links", a, b); err != nil || result.Changed || f.count() != beforeReplay {
		t.Fatalf("replay duplicated removal audit: %+v %v", result, err)
	}
}
func TestCLIValidation(t *testing.T) {
	for _, args := range [][]string{{}, {"unknown"}, {"link", "--suggest"}, {"link", "--tenant", "links", "--suggest", "--from", "x"}, {"unlink", "--tenant", "links", "--from", "a", "--to", "b"}, {"link", "--tenant", "links", "--from", "a"}, {"link", "--tenant", "links", "extra"}} {
		if err := Run(t.Context(), nil, args, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
