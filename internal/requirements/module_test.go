// SPDX-License-Identifier: AGPL-3.0-only

package requirements

import (
	"context"
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
	"github.com/inspr-at/paimos/internal/releases"
	"github.com/inspr-at/paimos/internal/tenant"
)

type fixture struct {
	t                    *testing.T
	db                   *dbtest.DB
	mux                  *http.ServeMux
	person, agent, other tenant.Principal
	project, release     string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, db: dbtest.Open(t), mux: http.NewServeMux()}
	// Bootstrap is the only operation that precedes tenant creation; even test
	// queries are wrapped in InTenant rather than relying on superuser bypass.
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.Admin, "00000000-0000-0000-0000-000000000001", func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('walker-a','Walker A') RETURNING id::text`).Scan(&f.person.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('walker-b','Walker B') RETURNING id::text`).Scan(&f.other.TenantID)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []*tenant.Principal{&f.person, &f.agent, &f.other} {
		p.Kind = tenant.Person
		p.Name = "Test person"
		if p == &f.agent {
			p.Kind = tenant.Agent
			p.Name = "Test agent"
			p.TenantID = f.person.TenantID
		}
		err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, p.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,$3) RETURNING id::text`, p.TenantID, string(p.Kind), p.Name).Scan(&p.ID)
		})
		if err != nil {
			t.Fatal(err)
		}
		// Handlers see project data only through a binding (ADR-003 P2).
		dbtest.BindRole(t, f.db, p.TenantID, p.ID, "member")
	}
	f.tx(func(tx pgx.Tx) error {
		var err error
		if _, err = tx.Exec(t.Context(), `SELECT aeon_seed_requirement_kind($1)`, f.person.TenantID); err != nil {
			return err
		}
		// Project has no parent. Use R1 allocator and event through fixture SQL.
		if err = tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Project' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&f.project); err != nil {
			return err
		}
		if _, err = tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id,brief_confirmed_at) VALUES($1,$2,now())`, f.person.TenantID, f.project); err != nil {
			return err
		}
		f.release, err = newNode(t.Context(), tx, f.person, "release", f.project, "Release one", "", nil)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number) VALUES($1,$2,$3,1)`, f.person.TenantID, f.release, f.project); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE journey_projects SET current_release_node_id=$2 WHERE project_node_id=$1`, f.project, f.release)
		return err
	})
	New(f.db.App).Mount(f.mux)
	releases.New(f.db.App).Mount(f.mux)
	return f
}
func (f *fixture) tx(fn func(pgx.Tx) error) {
	f.t.Helper()
	if err := db.InTenant(dbtest.Seed(f.t.Context()), f.db.App, f.person.TenantID, fn); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) call(p tenant.Principal, method, path string, body any) (int, []byte) {
	var data []byte
	if s, ok := body.(string); ok {
		data = []byte(s)
	} else if body != nil {
		data, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	if p.ID != "" {
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}
func (f *fixture) path() string { return "/api/projects/" + f.project + "/requirements" }
func (f *fixture) releasePath(suffix string) string {
	return "/api/projects/" + f.project + "/releases/" + f.release + "/" + suffix
}
func (f *fixture) revision() int64 {
	var rev int64
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `SELECT revision FROM journey_projects WHERE project_node_id=$1`, f.project).Scan(&rev)
	})
	return rev
}
func (f *fixture) add(kind, title string) Requirement {
	f.t.Helper()
	body := "Requirement body"
	in := createInput{Kind: kind, Title: title, Body: &body, Revision: f.revision(), Key: title}
	code, raw := f.call(f.person, "POST", f.path(), in)
	if code != 201 {
		f.t.Fatalf("create %d %s", code, raw)
	}
	var out Requirement
	if err := json.Unmarshal(raw, &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}
func (f *fixture) approve() agreeInput {
	f.t.Helper()
	in := agreeInput{Revision: f.revision(), Key: fmt.Sprintf("agreement-%d", f.revision())}
	f.tx(func(tx pgx.Tx) error {
		digest, err := Digest(f.t.Context(), tx, f.project)
		if err != nil {
			return err
		}
		if err = tx.QueryRow(f.t.Context(), `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,resource_id,rationale,expires_at) VALUES($1,$2,$2,$3,'node',$4,'Agree requirements',now()+interval '1 hour') RETURNING id::text`, f.person.TenantID, f.agent.ID, ApprovalScope(in.Revision, digest), f.project).Scan(&in.ApprovalID); err != nil {
			return err
		}
		if _, err = tx.Exec(f.t.Context(), `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) VALUES($1,$2,$3,'approved')`, f.person.TenantID, in.ApprovalID, f.person.ID); err != nil {
			return err
		}
		_, err = tx.Exec(f.t.Context(), `INSERT INTO agent_permission_grants(tenant_id,approval_request_id,agent_principal_id,scope,resource_kind,resource_id,valid_until) SELECT tenant_id,id,agent_principal_id,scope,resource_kind,resource_id,expires_at FROM approval_requests WHERE id=$1`, in.ApprovalID)
		return err
	})
	return in
}
func (f *fixture) agreement(in agreeInput) []Requirement {
	f.t.Helper()
	code, raw := f.call(f.person, "POST", f.path()+"/agree", in)
	if code != 200 {
		f.t.Fatalf("agree %d %s", code, raw)
	}
	var out []Requirement
	if err := json.Unmarshal(raw, &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}
func (f *fixture) walker() releases.Walker {
	f.t.Helper()
	code, raw := f.call(f.person, "GET", f.releasePath("walker"), nil)
	if code != 200 {
		f.t.Fatalf("walker %d %s", code, raw)
	}
	var out releases.Walker
	if err := json.Unmarshal(raw, &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}
func (f *fixture) suggestions(req Requirement, accepted bool) {
	f.tx(func(tx pgx.Tx) error {
		var draft string
		err := tx.QueryRow(f.t.Context(), `INSERT INTO intake_drafts(tenant_id,project_node_id,kind,requirement_kind,target_node_id,title,body,base_event_id,proposed_by_principal_id,idempotency_key)
  SELECT $1,$2,'requirement','functional',$3,'Accepted breakdown','Text',max(id),$4,($3::uuid)::text FROM events WHERE node_id=$3::uuid RETURNING id::text`, f.person.TenantID, f.project, req.NodeID, f.agent.ID).Scan(&draft)
		if err != nil {
			return err
		}
		_, err = tx.Exec(f.t.Context(), `INSERT INTO intake_draft_ticket_suggestions(tenant_id,draft_id,project_node_id,ordinal,title,estimated_hours,later,access_change) VALUES($1,$2,$3,0,'First ticket',2,false,true),($1,$2,$3,1,'Later ticket',3,true,false)`, f.person.TenantID, draft, f.project)
		if err != nil {
			return err
		}
		if accepted {
			_, err = tx.Exec(f.t.Context(), `INSERT INTO intake_draft_acceptances(tenant_id,draft_id,project_node_id,accepted_by_principal_id,target_node_id,event_id) SELECT $1,$2,$3,$4,$5,max(id) FROM events WHERE node_id=$5`, f.person.TenantID, draft, f.project, f.person.ID, req.NodeID)
			if err != nil {
				return err
			}
		}
		_, err = tx.Exec(f.t.Context(), `UPDATE journey_requirements SET origin_draft_id=$2 WHERE requirement_node_id=$1`, req.NodeID, draft)
		return err
	})
}
func (f *fixture) count(table string) int {
	f.t.Helper()
	var n int
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&n)
	})
	return n
}

func TestRequirementCreation(t *testing.T) {
	f := setup(t)
	body := "Details"
	in := createInput{Kind: "functional", Title: "Export", Body: &body, Revision: 1, Key: "create-export"}
	code, raw := f.call(f.person, "POST", f.path(), in)
	if code != 201 {
		t.Fatalf("create %d %s", code, raw)
	}
	events := f.count("events")
	code, repeated := f.call(f.person, "POST", f.path(), in)
	if code != 201 || string(raw) != string(repeated) || f.count("events") != events {
		t.Fatalf("replay %d %s", code, repeated)
	}
	in.Title = "Changed"
	if code, _ = f.call(f.person, "POST", f.path(), in); code != 409 {
		t.Fatalf("divergent replay %d", code)
	}
	in.Key = "new"
	if code, _ = f.call(f.person, "POST", f.path(), in); code != 409 {
		t.Fatalf("stale revision %d", code)
	}
	for _, tc := range []struct {
		p      tenant.Principal
		method string
		body   any
		want   int
	}{{f.agent, "POST", in, 403}, {f.other, "GET", nil, 404}, {tenant.Principal{}, "GET", nil, 401}, {f.person, "POST", `{"kind":"functional","title":"x","expected_revision":2,"idempotency_key":"missing-body"}`, 400}, {f.person, "POST", `{} {}`, 400}} {
		if got, b := f.call(tc.p, tc.method, f.path(), tc.body); got != tc.want {
			t.Errorf("want %d got %d %s", tc.want, got, b)
		}
	}
	if code, _ = f.call(f.agent, "GET", f.path(), nil); code != 200 {
		t.Fatalf("agent read %d", code)
	}
}

func TestAgreementGeneratesOnlyAcceptedWork(t *testing.T) {
	f := setup(t)
	accepted := f.add("functional", "Accepted")
	empty := f.add("functional", "Empty")
	unaccepted := f.add("functional", "Unaccepted")
	f.add("nonfunctional", "Accessibility")
	f.suggestions(accepted, true)
	f.suggestions(unaccepted, false)
	in := f.approve()
	items := f.agreement(in)
	if len(items) != 4 || f.count("journey_features") != 3 || f.count("journey_tickets") != 2 {
		t.Fatalf("generation: %+v", items)
	}
	for _, r := range items {
		if r.Status != "agreed" {
			t.Fatal(r)
		}
		if (r.NodeID == empty.NodeID || r.NodeID == unaccepted.NodeID) && len(r.TicketIDs) != 0 {
			t.Fatal("invented tickets")
		}
	}
	beforeEvents := f.count("events")
	f.agreement(in)
	if f.count("events") != beforeEvents {
		t.Fatal("replay wrote events")
	}
	walk := f.walker()
	if len(walk.Tickets) != 2 || len(walk.Features) != 3 || !walk.Tickets[0].Included || walk.Tickets[1].Included {
		t.Fatalf("walker %+v", walk)
	}
	foundSome := false
	for _, feature := range walk.Features {
		if feature.Selection == "some" {
			foundSome = feature.IncludedCount == 1 && feature.OpenCount == 2
		}
	}
	if !foundSome {
		t.Fatal("missing tri-state some")
	}
	var memories int
	var pinned, current string
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='memory'`).Scan(&memories); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT agreed_requirements_digest_sha256 FROM journey_projects WHERE project_node_id=$1`, f.project).Scan(&pinned); err != nil {
			return err
		}
		var err error
		current, err = Digest(t.Context(), tx, f.project)
		return err
	})
	if memories != 1 || pinned != current {
		t.Fatalf("memory=%d digest pin matches=%v", memories, pinned == current)
	}
	// Editable R1 fields are not authoritative generation lineage.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{}' WHERE id IN (SELECT ticket_node_id FROM journey_tickets)`)
		return err
	})
	// A fresh human re-agreement cannot duplicate immutable accepted suggestions.
	f.agreement(f.approve())
	if f.count("journey_tickets") != 2 || f.count("journey_features") != 3 {
		t.Fatal("duplicate generation")
	}
}

func TestAgreementRejectsStaleAndRevokedAuthority(t *testing.T) {
	for _, mode := range []string{"content", "revision", "revoked", "expired", "wrong_actor", "not_ready", "building"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			req := f.add("functional", "Feature")
			in := f.approve()
			f.tx(func(tx pgx.Tx) error {
				var err error
				switch mode {
				case "content":
					_, err = tx.Exec(t.Context(), `UPDATE nodes SET body='Person edit' WHERE id=$1`, req.NodeID)
				case "revision":
					_, err = tx.Exec(t.Context(), `UPDATE journey_projects SET revision=revision+1 WHERE project_node_id=$1`, f.project)
				case "revoked":
					_, err = tx.Exec(t.Context(), `UPDATE agent_permission_grants SET revoked_at=now() WHERE approval_request_id=$1`, in.ApprovalID)
				case "expired":
					_, err = tx.Exec(t.Context(), `UPDATE agent_permission_grants SET valid_until=now()-interval '1 second' WHERE approval_request_id=$1`, in.ApprovalID)
				case "wrong_actor":
					err = tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Different person') RETURNING id::text`, f.person.TenantID).Scan(&f.person.ID)
					if err == nil {
						_, err = tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE tenant_id=$1 AND key='member'`, f.person.TenantID, f.person.ID)
					}
				case "building":
					_, err = tx.Exec(t.Context(), `UPDATE journey_releases SET state='building' WHERE release_node_id=$1`, f.release)
				case "not_ready":
					_, err = tx.Exec(t.Context(), `UPDATE journey_projects SET profile='professional',decision='pending' WHERE project_node_id=$1`, f.project)
				}
				return err
			})
			before := f.count("events")
			code, raw := f.call(f.person, "POST", f.path()+"/agree", in)
			want := 403
			if mode == "revision" || mode == "not_ready" || mode == "building" {
				want = 409
			}
			if code != want {
				t.Fatalf("want %d got %d %s", want, code, raw)
			}
			if f.count("journey_features") != 0 || f.count("events") != before {
				t.Fatal("rejected agreement mutated state")
			}
		})
	}
}

func TestAgreementAtomicEventFailure(t *testing.T) {
	f := setup(t)
	f.add("functional", "Feature")
	in := f.approve()
	beforeNodes, beforeEvents := f.count("nodes"), f.count("events")
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.Admin, f.person.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE FUNCTION reject_agreement_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='journey.requirements_agreed' THEN RAISE EXCEPTION 'test event failure'; END IF; RETURN NEW; END; $$`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `CREATE TRIGGER reject_agreement_event BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_agreement_event()`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	code, _ := f.call(f.person, "POST", f.path()+"/agree", in)
	if code != 500 {
		t.Fatalf("event failure %d", code)
	}
	if f.count("nodes") != beforeNodes || f.count("events") != beforeEvents || f.count("journey_features") != 0 || f.revision() != in.Revision {
		t.Fatal("event failure did not roll back generation")
	}
}

func TestReleasePlanAndWalker(t *testing.T) {
	f := setup(t)
	req := f.add("functional", "Feature")
	f.suggestions(req, true)
	f.agreement(f.approve())
	walk := f.walker()
	a, b := walk.Tickets[0].NodeID, walk.Tickets[1].NodeID
	var screen string
	f.tx(func(tx pgx.Tx) error {
		var err error
		if _, err = tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,field_schema) VALUES($1,'screen','Screen','SCR','screen','{}')`, f.person.TenantID); err != nil {
			return err
		}
		screen, err = newNode(t.Context(), tx, f.person, "screen", f.project, "Screen", "", nil)
		if err != nil {
			return err
		}
		return link(t.Context(), tx, f.person, a, screen, "cites")
	})
	walk = f.walker()
	if len(walk.Tickets[0].ScreenIDs) != 1 || walk.Tickets[0].ScreenIDs[0] != screen {
		t.Fatal("linked screen missing")
	}
	plan := map[string]any{"expected_revision": walk.Revision, "ordered_ticket_ids": []string{b, a}, "included_ticket_ids": []string{a, b}}
	code, raw := f.call(f.person, "PUT", f.releasePath("plan"), plan)
	if code != 200 {
		t.Fatalf("plan %d %s", code, raw)
	}
	walk = f.walker()
	if walk.Tickets[0].NodeID != b || walk.Tickets[0].Position != 0 || walk.Features[0].Selection != "all" {
		t.Fatalf("plan not persisted %+v", walk)
	}
	if code, _ = f.call(f.person, "PUT", f.releasePath("plan"), plan); code != 409 {
		t.Fatalf("stale plan %d", code)
	}
	if code, _ = f.call(f.agent, "PUT", f.releasePath("plan"), plan); code != 403 {
		t.Fatalf("agent plan %d", code)
	}
	if code, _ = f.call(f.other, "GET", f.releasePath("walker"), nil); code != 404 {
		t.Fatalf("foreign read %d", code)
	}
	for _, bad := range []map[string]any{
		{"expected_revision": walk.Revision, "ordered_ticket_ids": []string{a}, "included_ticket_ids": []string{a}},
		{"expected_revision": walk.Revision, "ordered_ticket_ids": []string{a, b}, "included_ticket_ids": []string{f.project}},
		{"expected_revision": walk.Revision, "ordered_ticket_ids": []string{a, a}, "included_ticket_ids": []string{a}},
	} {
		code, _ = f.call(f.person, "PUT", f.releasePath("plan"), bad)
		if code != 409 && code != 400 {
			t.Fatalf("invalid plan %d", code)
		}
	}
	plan["expected_revision"] = walk.Revision
	plan["included_ticket_ids"] = []string{}
	if code, raw = f.call(f.person, "PUT", f.releasePath("plan"), plan); code != 200 {
		t.Fatalf("clear %d %s", code, raw)
	}
	walk = f.walker()
	if walk.Features[0].Selection != "none" {
		t.Fatal("tri-state none missing")
	}
	var access bool
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT access_required FROM journey_releases WHERE release_node_id=$1`, f.release).Scan(&access)
	})
	if access {
		t.Fatal("stale access requirement")
	}
	plan["expected_revision"] = walk.Revision
	plan["included_ticket_ids"] = []string{a}
	if code, _ = f.call(f.person, "PUT", f.releasePath("plan"), plan); code != 200 {
		t.Fatal(code)
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='building' WHERE release_node_id=$1`, f.release)
		return err
	})
	if code, _ = f.call(f.person, "PUT", f.releasePath("plan"), plan); code != 409 {
		t.Fatalf("building plan %d", code)
	}
	if len(f.walker().Tickets) != 1 {
		t.Fatal("build walker includes unrelated backlog")
	}
}

func TestConcurrentPlanRevision(t *testing.T) {
	f := setup(t)
	req := f.add("functional", "Feature")
	f.suggestions(req, true)
	f.agreement(f.approve())
	walk := f.walker()
	plan := map[string]any{"expected_revision": walk.Revision, "ordered_ticket_ids": []string{walk.Tickets[0].NodeID, walk.Tickets[1].NodeID}, "included_ticket_ids": []string{}}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := f.call(f.person, "PUT", f.releasePath("plan"), plan)
			codes <- code
		}()
	}
	wg.Wait()
	close(codes)
	got := map[int]int{}
	for code := range codes {
		got[code]++
	}
	if got[200] != 1 || got[409] != 1 {
		t.Fatalf("concurrent writes %v", got)
	}
}

// Compile-time check that external journey code can consume the digest helper
// without obtaining an unscoped pool or importing this package's handlers.
var _ func(context.Context, pgx.Tx, string) (string, error) = Digest

func TestManualScopeRequiresFreshAgreement(t *testing.T) {
	f := setup(t)
	f.add("functional", "Feature")
	in := f.approve()
	var manual string
	f.tx(func(tx pgx.Tx) error {
		var err error
		manual, err = newNode(t.Context(), tx, f.person, "ticket", f.project, "Extra scope", "Manual", nil)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,walker_position,source,scope_revision_required) VALUES($1,$2,$3,0,'manual',true)`, f.person.TenantID, manual, f.project)
		return err
	})
	if code, _ := f.call(f.person, "POST", f.path()+"/agree", in); code != 403 {
		t.Fatalf("stale manual scope approval %d", code)
	}
	// The new scope gets a different approval even though direct R1 work did not
	// advance the project projection revision.
	fresh := f.approve()
	fresh.Key = "manual-agreement"
	f.agreement(fresh)
	var needsRevision bool
	var pin, current string
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT scope_revision_required FROM journey_tickets WHERE ticket_node_id=$1`, manual).Scan(&needsRevision); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT agreed_requirements_digest_sha256 FROM journey_projects WHERE project_node_id=$1`, f.project).Scan(&pin); err != nil {
			return err
		}
		var err error
		current, err = Digest(t.Context(), tx, f.project)
		return err
	})
	if needsRevision || pin != current {
		t.Fatal("fresh manual agreement did not clear pending scope atomically")
	}
}

func TestHistoricalReleaseCannotBeReplannedOrStolen(t *testing.T) {
	f := setup(t)
	req := f.add("functional", "Feature")
	f.suggestions(req, true)
	f.agreement(f.approve())
	old := f.walker()
	oldRelease := f.release
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='released',released_at=now(),version_scheme='legacy',version='1.0.0' WHERE release_node_id=$1`, oldRelease); err != nil {
			return err
		}
		var err error
		f.release, err = newNode(t.Context(), tx, f.person, "release", f.project, "Release two", "", nil)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number) VALUES($1,$2,$3,2)`, f.person.TenantID, f.release, f.project); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE journey_projects SET current_release_node_id=$2 WHERE project_node_id=$1`, f.project, f.release)
		return err
	})
	next := f.walker()
	if len(next.Tickets) != 1 || next.Tickets[0].NodeID != old.Tickets[1].NodeID {
		t.Fatal("historical ticket leaked into next release choices")
	}
	plan := map[string]any{"expected_revision": next.Revision, "ordered_ticket_ids": []string{next.Tickets[0].NodeID}, "included_ticket_ids": []string{old.Tickets[0].NodeID}}
	if code, _ := f.call(f.person, "PUT", f.releasePath("plan"), plan); code != 409 {
		t.Fatalf("steal historical member %d", code)
	}
	plan["included_ticket_ids"] = []string{next.Tickets[0].NodeID}
	if code, raw := f.call(f.person, "PUT", f.releasePath("plan"), plan); code != 200 {
		t.Fatalf("next plan %d %s", code, raw)
	}
	f.release = oldRelease
	historical := f.walker()
	if historical.State != "released" || len(historical.Tickets) != 1 || historical.Tickets[0].NodeID != old.Tickets[0].NodeID || historical.Tickets[0].Position != old.Tickets[0].Position {
		t.Fatal("history changed")
	}
	if code, _ := f.call(f.person, "PUT", f.releasePath("plan"), plan); code != 409 {
		t.Fatalf("historical plan %d", code)
	}
}

func TestPlanEventFailureRollsBackSelectionAndRevisions(t *testing.T) {
	f := setup(t)
	req := f.add("functional", "Feature")
	f.suggestions(req, true)
	f.agreement(f.approve())
	before := f.walker()
	projectRev := f.revision()
	eventCount := f.count("events")
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.Admin, f.person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `CREATE FUNCTION reject_plan_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='journey.release_planned' THEN RAISE EXCEPTION 'test event failure'; END IF; RETURN NEW; END; $$`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_plan_event BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_plan_event()`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := map[string]any{"expected_revision": before.Revision, "ordered_ticket_ids": []string{before.Tickets[1].NodeID, before.Tickets[0].NodeID}, "included_ticket_ids": []string{}}
	if code, _ := f.call(f.person, "PUT", f.releasePath("plan"), plan); code != 500 {
		t.Fatalf("event failure %d", code)
	}
	after := f.walker()
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if string(a) != string(b) || f.revision() != projectRev || f.count("events") != eventCount {
		t.Fatal("plan was not rolled back")
	}
}

func TestDigestHoldsContentStableUntilCommit(t *testing.T) {
	f := setup(t)
	req := f.add("functional", "Feature")
	ready := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
			if _, err := lockProject(t.Context(), tx, f.project, true); err != nil {
				close(ready)
				return err
			}
			if _, err := Digest(t.Context(), tx, f.project); err != nil {
				close(ready)
				return err
			}
			close(ready)
			<-release
			return nil
		})
	}()
	<-ready
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SET LOCAL lock_timeout='50ms'`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET body='Concurrent edit' WHERE id=$1`, req.NodeID)
		return err
	})
	close(release)
	if first := <-done; first != nil {
		t.Fatal(first)
	}
	if err == nil || !strings.Contains(err.Error(), "55P03") {
		t.Fatalf("content edit was not fenced by digest row lock: %v", err)
	}
	// The rejected edit was not applied; after agreement's transaction releases
	// its locks, a normal R1 edit is allowed and invalidates the digest.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET body='Later edit' WHERE id=$1`, req.NodeID)
		return err
	})
}

func TestWorkParentStatusRequirementGeneration(t *testing.T) {
	f := setup(t)
	dbtest.EnableWorkParentStatus(t, f.db, f.person.TenantID)
	accepted := f.add("functional", "Accepted work")
	f.suggestions(accepted, true)
	items := f.agreement(f.approve())
	if len(items) != 1 || len(items[0].TicketIDs) != 2 {
		t.Fatalf("generation %+v", items)
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='status_autopilot.derived'`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("unchanged initial parent emitted transitions %d", count)
		}
		var state string
		if err := tx.QueryRow(t.Context(), `SELECT state FROM nodes WHERE id=(SELECT parent_id FROM nodes WHERE id=$1)`, items[0].TicketIDs[0]).Scan(&state); err != nil {
			return err
		}
		if state != "open" {
			t.Fatalf("generated parent state %s", state)
		}
		return nil
	})
}
