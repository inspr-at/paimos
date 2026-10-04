// SPDX-License-Identifier: AGPL-3.0-only

package releases

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

type ticketFixture struct {
	t                         *testing.T
	db                        *dbtest.DB
	mux                       *http.ServeMux
	person, agent, other      tenant.Principal
	project, release, feature string
}

func ticketSetup(t *testing.T) *ticketFixture {
	t.Helper()
	f := &ticketFixture{t: t, db: dbtest.Open(t), mux: http.NewServeMux()}
	for i, p := range []*tenant.Principal{&f.person, &f.other} {
		p.Kind = tenant.Person
		err := db.InTenant(dbtest.Seed(t.Context()), f.db.Admin, "00000000-0000-0000-0000-000000000001", func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,'Ticket tests') RETURNING id::text`, fmt.Sprintf("tickets-%d", i)).Scan(&p.TenantID)
		})
		if err != nil {
			t.Fatal(err)
		}
		err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, p.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Person') RETURNING id::text`, p.TenantID).Scan(&p.ID)
		})
		if err != nil {
			t.Fatal(err)
		}
		// Handlers see project data only through a binding (ADR-003 P2).
		dbtest.BindRole(t, f.db, p.TenantID, p.ID, "member")
	}
	f.agent = tenant.Principal{TenantID: f.person.TenantID, Kind: tenant.Agent}
	f.tx(func(tx pgx.Tx) error {
		ctx := t.Context()
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Agent') RETURNING id::text`, f.person.TenantID).Scan(&f.agent.ID); err != nil {
			return err
		}
		if err := dbtest.BindLegacyTx(ctx, tx, f.person.TenantID, f.agent.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE tenant_id=$1 AND key='member'`, f.person.TenantID, f.agent.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT aeon_seed_requirement_kind($1)`, f.person.TenantID); err != nil {
			return err
		}
		node := func(kind string, parent any) (string, error) {
			var id string
			err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,parent_id,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),$2,$3 FROM node_kinds WHERE slug=$3 RETURNING id::text`, f.person.TenantID, parent, kind).Scan(&id)
			return id, err
		}
		var err error
		if f.project, err = node("project", nil); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2)`, f.person.TenantID, f.project); err != nil {
			return err
		}
		if f.release, err = node("release", f.project); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number) VALUES($1,$2,$3,1)`, f.person.TenantID, f.release, f.project); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE journey_projects SET current_release_node_id=$2 WHERE project_node_id=$1`, f.project, f.release); err != nil {
			return err
		}
		req, err := node("requirement", f.project)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO journey_requirements(tenant_id,requirement_node_id,project_node_id,kind,revision) VALUES($1,$2,$3,'functional',1)`, f.person.TenantID, req, f.project); err != nil {
			return err
		}
		if f.feature, err = node("work", f.project); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO journey_features(tenant_id,feature_node_id,project_node_id,requirement_node_id) VALUES($1,$2,$3,$4)`, f.person.TenantID, f.feature, f.project, req)
		return err
	})
	New(f.db.App).Mount(f.mux)
	return f
}
func (f *ticketFixture) tx(fn func(pgx.Tx) error) {
	f.t.Helper()
	if err := db.InTenant(dbtest.Seed(f.t.Context()), f.db.App, f.person.TenantID, fn); err != nil {
		f.t.Fatal(err)
	}
}
func (f *ticketFixture) path() string {
	return "/api/projects/" + f.project + "/releases/" + f.release + "/tickets"
}
func (f *ticketFixture) call(p tenant.Principal, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, f.path(), strings.NewReader(body))
	if p.ID != "" {
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}
func (f *ticketFixture) body(rev int, key string, included bool, feature string) string {
	in := map[string]any{"title": "New ticket", "expected_revision": rev, "idempotency_key": key, "included": included}
	if feature != "" {
		in["feature_node_id"] = feature
	}
	b, _ := json.Marshal(in)
	return string(b)
}
func ticketPlan(t *testing.T, w *httptest.ResponseRecorder) Walker {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var out Walker
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func (f *ticketFixture) counts() [5]int64 {
	var counts [5]int64
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `SELECT (SELECT count(*) FROM nodes),(SELECT count(*) FROM events),(SELECT count(*) FROM journey_action_receipts),(SELECT revision FROM journey_releases WHERE release_node_id=$1),(SELECT revision FROM journey_projects WHERE project_node_id=$2)`, f.release, f.project).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4])
	})
	return counts
}

func TestCreateReleaseTickets(t *testing.T) {
	f := ticketSetup(t)
	for i, tc := range []struct{ included, feature bool }{{true, false}, {false, false}, {true, true}, {false, true}} {
		feature := ""
		parent := f.project
		if tc.feature {
			feature = f.feature
			parent = feature
		}
		before := f.counts()
		body := f.body(i+1, fmt.Sprint(i), tc.included, feature)
		if i == 0 {
			body = strings.Replace(body, `"included":true,`, "", 1)
		} // Default inclusion.
		out := ticketPlan(t, f.call(f.person, body))
		if out.Revision != int64(i+2) || len(out.Tickets) != i+1 {
			t.Fatalf("plan %+v", out)
		}
		ticket := out.Tickets[i]
		if ticket.Included != tc.included || ticket.Position != i || ticket.Title != "New ticket" || ticket.Key == "" {
			t.Fatalf("ticket %+v", ticket)
		}
		if (ticket.FeatureID != nil) != tc.feature || (tc.feature && *ticket.FeatureID != feature) {
			t.Fatalf("feature %+v", ticket)
		}
		f.tx(func(tx pgx.Tx) error {
			var actualParent, source string
			var scope bool
			err := tx.QueryRow(t.Context(), `SELECT n.parent_id::text,j.source,j.scope_revision_required FROM nodes n JOIN journey_tickets j ON j.ticket_node_id=n.id AND j.tenant_id=n.tenant_id WHERE n.id=$1`, ticket.NodeID).Scan(&actualParent, &source, &scope)
			if err == nil && (actualParent != parent || source != "manual" || !scope) {
				t.Errorf("parent=%s source=%s scope=%v", actualParent, source, scope)
			}
			return err
		})
		after := f.counts()
		if after != [5]int64{before[0] + 1, before[1] + 2, before[2] + 1, before[3] + 1, before[4] + 1} {
			t.Fatalf("non-atomic counts %v -> %v", before, after)
		}
		retry := ticketPlan(t, f.call(f.person, body))
		if retry.Revision != out.Revision || f.counts() != after {
			t.Fatal("retry changed state")
		}
	}
}

func TestCreateReleaseTicketFailures(t *testing.T) {
	f := ticketSetup(t)
	body := f.body(1, "create", true, "")
	baseline := f.counts()
	cases := []struct {
		name   string
		p      tenant.Principal
		body   string
		status int
	}{
		{"anonymous", tenant.Principal{}, body, 401},
		{"agent", f.agent, body, 403},
		{"tenant", f.other, body, 404},
		{"stale", f.person, f.body(9, "stale", true, ""), 409},
		{"missing feature", f.person, f.body(1, "missing", true, "00000000-0000-0000-0000-000000000099"), 404},
		{"wrong feature", f.person, f.body(1, "wrong", true, f.release), 404},
		{"empty title", f.person, strings.Replace(body, "New ticket", "  ", 1), 400},
		{"unknown field", f.person, strings.Replace(body, "{", `{"other":1,`, 1), 400},
		{"null inclusion", f.person, strings.Replace(body, `"included":true`, `"included":null`, 1), 400},
		{"trailing body", f.person, body + "{}", 400},
	}
	forged := f.agent
	forged.Kind = tenant.Person
	cases = append(cases, struct {
		name   string
		p      tenant.Principal
		body   string
		status int
	}{"forged person", forged, body, 403})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := f.call(tc.p, tc.body)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if f.counts() != baseline {
				t.Fatal("rejection changed state")
			}
		})
	}
	ticketPlan(t, f.call(f.person, body))
	after := f.counts()
	if w := f.call(f.person, strings.Replace(body, "New ticket", "Different", 1)); w.Code != 409 {
		t.Fatalf("divergent retry %d %s", w.Code, w.Body.String())
	}
	if f.counts() != after {
		t.Fatal("conflict changed state")
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='building' WHERE release_node_id=$1`, f.release)
		return err
	})
	if w := f.call(f.person, f.body(2, "after-build", true, "")); w.Code != 409 || !strings.Contains(w.Body.String(), "planning") {
		t.Fatalf("non-planning %d %s", w.Code, w.Body.String())
	}
	ticketPlan(t, f.call(f.person, body)) // A retry of a completed mutation remains safe.
	if f.counts() != after {
		t.Fatal("retry after build changed state")
	}
}

func TestCreateReleaseTicketConcurrentRetryAndRollback(t *testing.T) {
	f := ticketSetup(t)
	body := f.body(1, "concurrent", true, "")
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- f.call(f.person, body) }()
	}
	wg.Wait()
	close(results)
	var id string
	for result := range results {
		out := ticketPlan(t, result)
		if len(out.Tickets) != 1 {
			t.Fatal("duplicate tickets")
		}
		if id != "" && id != out.Tickets[0].NodeID {
			t.Fatal("retry returned a different ticket")
		}
		id = out.Tickets[0].NodeID
	}
	if got := f.counts(); got[1] != 2 || got[2] != 1 || got[3] != 2 {
		t.Fatalf("retry counts %v", got)
	}
	// Fail the last event append; the preceding node/event/projections must roll back.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `ALTER TABLE events ADD CONSTRAINT reject_plan_test CHECK(type<>'journey.release_planned') NOT VALID`)
		return err
	})
	baseline := f.counts()
	if w := f.call(f.person, f.body(2, "rollback", true, "")); w.Code != 500 {
		t.Fatalf("event failure %d %s", w.Code, w.Body.String())
	}
	if f.counts() != baseline {
		t.Fatal("event failure left partial state")
	}
}

func TestCreateReleaseTicketParentAndSchemaRules(t *testing.T) {
	f := ticketSetup(t)
	baseline := f.counts()
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET allowed_child_kinds='{}' WHERE slug='work'`)
		return err
	})
	if w := f.call(f.person, f.body(1, "parent", true, f.feature)); w.Code != 409 {
		t.Fatalf("parent rule %d %s", w.Code, w.Body.String())
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema='{"type":"object","required":["priority"]}' WHERE slug='work'`)
		return err
	})
	if w := f.call(f.person, f.body(1, "schema", true, "")); w.Code != 409 {
		t.Fatalf("schema rule %d %s", w.Code, w.Body.String())
	}
	if f.counts() != baseline {
		t.Fatal("validation failure changed state")
	}
}

func TestWorkParentStatusReleaseQuickCreate(t *testing.T) {
	f := ticketSetup(t)
	dbtest.EnableWorkParentStatus(t, f.db, f.person.TenantID)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='done' WHERE id=$1`, f.feature)
		return err
	})
	result := ticketPlan(t, f.call(f.person, f.body(1, "work-child", true, f.feature)))
	if len(result.Tickets) != 1 {
		t.Fatalf("created tickets %+v", result.Tickets)
	}
	f.tx(func(tx pgx.Tx) error {
		var state, slug string
		if err := tx.QueryRow(t.Context(), `SELECT state FROM nodes WHERE id=$1`, f.feature).Scan(&state); err != nil {
			return err
		}
		if state != "open" {
			t.Fatalf("quick-create parent state %s", state)
		}
		if err := tx.QueryRow(t.Context(), `SELECT k.slug FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.id=$1`, result.Tickets[0].NodeID).Scan(&slug); err != nil {
			return err
		}
		if slug != "work" {
			t.Fatalf("kind %s", slug)
		}
		return nil
	})
}
