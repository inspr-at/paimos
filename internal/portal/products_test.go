// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func productFixture(t *testing.T) *fixture {
	d := dbtest.Open(t)
	m := New(d.App, false, bytes.Repeat([]byte{19}, 32))
	mux := http.NewServeMux()
	m.Mount(mux)
	m.MountPublic(mux)
	return &fixture{t: t, m: m, d: d, h: mux}
}

func configureFixtureProduct(t *testing.T, d *dbtest.DB, tid, id, slug, policy string, published bool) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE portal_products SET slug=$2,participation_policy=$3,published=$4 WHERE product_id=$1::uuid`, id, slug, policy, published)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProductPortalDefaultAndIsolation(t *testing.T) {
	f := productFixture(t)
	d := f.d
	a := makeTenant(t, d, "products-a", "A")
	b := makeTenant(t, d, "products-b", "B")
	admin := makePerson(t, d, a, "Admin", "admin")
	member := makePerson(t, d, a, "Member", "member")
	p1 := insertNode(t, d, a, "PPR-1", "portal_product", "First", "first product", "published", "", "{}")
	p2 := insertNode(t, d, a, "PPR-2", "portal_product", "Second", "second product", "published", "", "{}")
	pb := insertNode(t, d, b, "PPR-1", "portal_product", "Other tenant", "private to B", "published", "", "{}")
	configureFixtureProduct(t, d, a, p1, "first", "legacy", true)
	configureFixtureProduct(t, d, a, p2, "second", "disabled", true)
	configureFixtureProduct(t, d, b, pb, "first", "legacy", true)
	projectB := insertNode(t, d, b, "PRJ-1", "project", "Other tenant project", "private to B", "active", "", "{}")
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, b, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO portal_product_pace(tenant_id,product_id,project_node_id,revision) VALUES($1::uuid,$2::uuid,$3::uuid,1)`, b, pb, projectB)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	setPortal(t, d, a, true)
	setPortal(t, d, b, true)
	insertNode(t, d, a, "PWS-1", "portal_wish", "Wish one", "public wish", "published", p1, "{}")
	insertNode(t, d, a, "PWS-2", "portal_wish", "Wish two", "other product wish", "published", p2, "{}")
	// Changing order cannot change the default or transfer its project link.
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, a, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET position=-10 WHERE id=$1::uuid`, p2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	base := "/api/public/portal/products-a"
	ip := "203.0.113.151:1"
	old := f.do("GET", base, "", ip, nil, nil, nil)
	if old.Code != 200 || !strings.Contains(old.Body.String(), `"title":"First"`) || strings.Contains(old.Body.String(), "Wish two") {
		t.Fatalf("default %d %s", old.Code, old.Body)
	}
	// A strict old consumer still accepts the exact old public shape.
	dec := json.NewDecoder(bytes.NewReader(old.Body.Bytes()))
	dec.DisallowUnknownFields()
	var doc portalDocument
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/catalog", "/catalog.json", "/wishes", "/comparison", "/pace", "/releases", "/roadmap", "/roadmap.json", "/llms.txt"} {
		rec := f.do("GET", base+"/products/second"+suffix, "", ip, nil, nil, nil)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "Wish one") || strings.Contains(rec.Body.String(), "Other tenant") {
			t.Fatalf("scoped %s: %d %s", suffix, rec.Code, rec.Body)
		}
	}
	missing := f.do("GET", base+"/products/missing", "", ip, nil, nil, nil)
	configureFixtureProduct(t, d, a, p2, "second", "disabled", false)
	sameResponse(t, missing, f.do("GET", base+"/products/second", "", ip, nil, nil, nil))
	configureFixtureProduct(t, d, a, p2, "second", "disabled", true)
	for _, path := range []string{base + "/wishes/PWS-2/votes", base + "/products/first/wishes/PWS-2/votes"} {
		rec := f.do("POST", path, "{}", ip, nil, nil, nil)
		if rec.Code != 404 {
			t.Fatalf("cross-product vote: %d %s", rec.Code, rec.Body)
		}
	}
	beforeWishes := wishCount(t, d, a)
	beforeVotes := voteCount(t, d, a)
	beforeCorrections := correctionCount(t, d, a)
	for _, entry := range []struct{ suffix, body string }{
		{"/wishes", `{"title":"Blocked","summary":"No write"}`},
		{"/wishes", `{"title":"Blocked","summary":"No write","website":"honeypot"}`},
		{"/wishes/PWS-2/votes", "{}"},
		{"/corrections", `{"competitor":"Example","aspect":"One","statement":"Correction statement","source_url":"https://example.com"}`},
	} {
		rec := f.do("POST", base+"/products/second"+entry.suffix, entry.body, ip, nil, nil, nil)
		if rec.Code != 403 || rec.Header().Get("Set-Cookie") != "" {
			t.Fatalf("disabled %s: %d %s", entry.suffix, rec.Code, rec.Body)
		}
	}
	if wishCount(t, d, a) != beforeWishes || voteCount(t, d, a) != beforeVotes || correctionCount(t, d, a) != beforeCorrections {
		t.Fatal("disabled participation wrote data")
	}
	path := "/api/portal/products/" + p2 + "/settings"
	if rec := f.do("GET", path, "", ip, &member, nil, nil); rec.Code != 403 {
		t.Fatalf("member settings %d", rec.Code)
	}
	if rec := f.do("GET", "/api/portal/products/"+pb+"/settings", "", ip, &admin, nil, nil); rec.Code != 404 {
		t.Fatalf("foreign settings %d", rec.Code)
	}
	var settings productSettings
	if err := json.Unmarshal(f.do("GET", path, "", ip, &admin, nil, nil).Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"revision":%d,"slug":"second","published":true,"is_default":true,"participation_policy":"disabled"}`, settings.Revision)
	if rec := f.do("PUT", path, body, ip, &admin, nil, nil); rec.Code != 200 {
		t.Fatalf("default change %d %s", rec.Code, rec.Body)
	}
	if rec := f.do("PUT", path, body, ip, &admin, nil, nil); rec.Code != 409 {
		t.Fatalf("stale settings %d %s", rec.Code, rec.Body)
	}
	old = f.do("GET", base, "", ip, nil, nil, nil)
	if old.Code != 200 || old.Body.String() == "" || !strings.Contains(old.Body.String(), `"title":"Second"`) {
		t.Fatalf("new default %d %s", old.Code, old.Body)
	}
	if rec := f.do("POST", base+"/wishes/PWS-2/votes", "{}", "203.0.113.152:1", nil, nil, nil); rec.Code != 403 {
		t.Fatalf("old URL bypass %d", rec.Code)
	}
	// Keep actual foreign rows behind both policies, so zero is evidence of
	// isolation rather than an empty fixture.
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, b, func(tx pgx.Tx) error {
		var products, links int
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM portal_products),(SELECT count(*) FROM portal_product_pace)`).Scan(&products, &links); err != nil {
			return err
		}
		if products != 1 || links != 1 {
			t.Fatalf("foreign RLS fixture has %d products and %d links", products, links)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, a, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM portal_products WHERE tenant_id=$1::uuid`, b).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("RLS exposed another tenant's products")
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM portal_product_pace WHERE tenant_id=$1::uuid`, b).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("RLS exposed another tenant's links")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProductRegisteredGateAndBallotHistory(t *testing.T) {
	f := productFixture(t)
	d := f.d
	tid := makeTenant(t, d, "history", "History")
	admin := makePerson(t, d, tid, "Admin", "admin")
	id := insertNode(t, d, tid, "PPR-1", "portal_product", "History product", "history", "published", "", "{}")
	configureFixtureProduct(t, d, tid, id, "history-product", "legacy", true)
	setPortal(t, d, tid, true)
	insertNode(t, d, tid, "PWS-1", "portal_wish", "Wish", "summary", "published", id, "{}")
	base := "/api/public/portal/history"
	ip := "203.0.113.160:1"
	rec := f.do("POST", base+"/wishes/PWS-1/votes", "{}", ip, nil, nil, nil)
	if rec.Code != 201 {
		t.Fatalf("legacy vote %d %s", rec.Code, rec.Body)
	}
	if rec := f.do("PUT", "/api/portal/products/"+id+"/settings", `{"revision":1,"slug":"history-product","published":true,"participation_policy":"registered"}`, ip, &admin, nil, nil); rec.Code != 409 || !strings.Contains(rec.Body.String(), "not ready") {
		t.Fatalf("activation %d %s", rec.Code, rec.Body)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE portal_products SET participation_policy='registered' WHERE product_id=$1::uuid`, id)
		return err
	})
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "23514" || pe.Message != "registered participation not ready" {
		t.Fatalf("DB gate: %v", err)
	}
	// Simulate the later reviewed activation in this disposable database only.
	if _, err := d.Admin.Exec(t.Context(), `CREATE OR REPLACE FUNCTION aeon_portal_registered_ready() RETURNS boolean LANGUAGE sql IMMUTABLE AS $$ SELECT true $$`); err != nil {
		t.Fatal(err)
	}
	configureFixtureProduct(t, d, tid, id, "history-product", "registered", true)
	for _, path := range []string{base, base + "/products/history-product"} {
		rec := f.do("POST", path+"/wishes/PWS-1/votes", "{}", "203.0.113.161:1", nil, nil, nil)
		if rec.Code != 401 || rec.Header().Get("Set-Cookie") != "" {
			t.Fatalf("registered ballot %d %s", rec.Code, rec.Body)
		}
		var doc portalDocument
		if err := json.Unmarshal(f.do("GET", path, "", ip, nil, nil, nil).Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Wishes) != 1 || doc.Wishes[0].Votes != 0 {
			t.Fatalf("legacy sum entered registered projection: %+v", doc.Wishes)
		}
		var p participation
		if err := json.Unmarshal(f.do("GET", path+"/participation", "", ip, nil, nil, nil).Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if p.Policy != "registered" || p.Voting || p.Intake || p.RegisteredAvailable || p.HistoryUntil == nil || len(p.History) != 1 || p.History[0].Votes != 1 {
			t.Fatalf("history %+v", p)
		}
	}
	if voteCount(t, d, tid) != 1 {
		t.Fatal("registered created an anonymous ballot")
	}
	configureFixtureProduct(t, d, tid, id, "history-product", "legacy", true)
	var doc portalDocument
	if err := json.Unmarshal(f.do("GET", base, "", ip, nil, nil, nil).Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Wishes) != 1 || doc.Wishes[0].Votes != 1 {
		t.Fatal("return to legacy did not preserve the ballot")
	}
}

func TestProductHistoryUsesBoundedKeysetPages(t *testing.T) {
	f := productFixture(t)
	d := f.d
	tid := makeTenant(t, d, "history-pages", "History pages")
	id := insertNode(t, d, tid, "PPR-1", "portal_product", "History", "summary", "published", "", "{}")
	configureFixtureProduct(t, d, tid, id, "history", "disabled", true)
	setPortal(t, d, tid, true)
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `WITH wishes AS (
            INSERT INTO nodes(tenant_id,key,kind_id,title,state,parent_id)
            SELECT $1::uuid,'PWS-'||n::text,k.id,'Public wish '||n::text,'published',$2::uuid
            FROM generate_series(1,102) n CROSS JOIN node_kinds k WHERE k.slug='portal_wish'
            RETURNING id)
            INSERT INTO portal_votes(tenant_id,wish_id,voter_hash,weight,product_id)
            SELECT $1::uuid,id,repeat('b',64),1,$2::uuid FROM wishes`, tid, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	base := "/api/public/portal/history-pages/participation"
	var first, second participation
	rec := f.do("GET", base, "", "203.0.113.165:1", nil, nil, nil)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &first) != nil || len(first.History) != 100 || first.NextCursor == "" {
		t.Fatalf("first history page %d %s", rec.Code, rec.Body)
	}
	rec = f.do("GET", base+"?after="+first.NextCursor, "", "203.0.113.165:1", nil, nil, nil)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &second) != nil || len(second.History) != 2 || second.NextCursor != "" {
		t.Fatalf("second history page %d %s", rec.Code, rec.Body)
	}
	seen := map[string]bool{}
	for _, page := range []participation{first, second} {
		for _, wish := range page.History {
			if seen[wish.WishKey] || wish.Votes != 1 {
				t.Fatalf("repeated/changed history %+v", wish)
			}
			seen[wish.WishKey] = true
		}
	}
	if len(seen) != 102 {
		t.Fatalf("history lost wishes: %d", len(seen))
	}
	rec = f.do("GET", base+"?after=invalid", "", "203.0.113.165:1", nil, nil, nil)
	if rec.Code != 400 {
		t.Fatalf("invalid cursor %d", rec.Code)
	}
}

func TestNewProductStartsClosedAndDisabled(t *testing.T) {
	f := productFixture(t)
	d := f.d
	tid := makeTenant(t, d, "pilot", "Pilot")
	setPortal(t, d, tid, true)
	var id string
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,state) SELECT $1::uuid,'PPR-1',id,'Pilot product','published' FROM node_kinds WHERE slug='portal_product' RETURNING id::text`, tid).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		p, err := loadProductSettings(t.Context(), tx, id, false)
		if err != nil {
			return err
		}
		if p.Published || p.Policy != "disabled" || !p.Default {
			t.Fatalf("new defaults %+v", p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rec := f.do("GET", "/api/public/portal/pilot/products/ppr-1", "", "203.0.113.162:1", nil, nil, nil)
	if rec.Code != 404 {
		t.Fatalf("new product published itself %d %s", rec.Code, rec.Body)
	}
}

func TestProductPolicyRacesParticipation(t *testing.T) {
	for _, route := range []string{"old-vote", "product-vote", "old-intake", "product-intake"} {
		for _, first := range []string{"policy", "participation"} {
			t.Run(route+"/"+first, func(t *testing.T) {
				f := productFixture(t)
				d := f.d
				tid := makeTenant(t, d, "race", "Race")
				admin := makePerson(t, d, tid, "Admin", "admin")
				id := insertNode(t, d, tid, "PPR-1", "portal_product", "Race product", "race", "published", "", "{}")
				configureFixtureProduct(t, d, tid, id, "race-product", "legacy", true)
				setPortal(t, d, tid, true)
				insertNode(t, d, tid, "PWS-1", "portal_wish", "Wish", "summary", "published", id, "{}")
				base := "/api/public/portal/race"
				if strings.HasPrefix(route, "product-") {
					base += "/products/race-product"
				}
				path := base + "/wishes/PWS-1/votes"
				body := "{}"
				isVote := strings.HasSuffix(route, "vote")
				if !isVote {
					path = base + "/wishes"
					body = `{"title":"New wish","summary":"Race intake"}`
				}
				table := "portal_products"
				operation := "UPDATE"
				if first == "participation" {
					operation = "INSERT"
					table = "portal_votes"
					if !isVote {
						table = "nodes"
					}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				// A database barrier pauses the first real HTTP write with its
				// tenant fence held. pg_blocking_pids proves the second overlapped.
				if _, err := d.Admin.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION pause_product_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(619); RETURN NEW; END $$; CREATE TRIGGER pause_product_write BEFORE %s ON %s FOR EACH ROW EXECUTE FUNCTION pause_product_write()`, operation, table)); err != nil {
					t.Fatal(err)
				}
				barrier, err := d.Admin.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer barrier.Rollback(context.Background())
				if _, err := barrier.Exec(ctx, `SELECT pg_advisory_xact_lock(619)`); err != nil {
					t.Fatal(err)
				}
				policyDone := make(chan *httptest.ResponseRecorder, 1)
				writeDone := make(chan *httptest.ResponseRecorder, 1)
				policy := func() {
					policyDone <- f.do("PUT", "/api/portal/products/"+id+"/settings", `{"revision":1,"slug":"race-product","published":true,"participation_policy":"disabled"}`, "203.0.113.170:1", &admin, nil, nil)
				}
				act := func() { writeDone <- f.do("POST", path, body, "203.0.113.171:1", nil, nil, nil) }
				if first == "policy" {
					go policy()
				} else {
					go act()
				}
				firstPID := waitProductBlocked(t, ctx, d, barrier.Conn().PgConn().PID())
				if first == "policy" {
					go act()
				} else {
					go policy()
				}
				waitProductBlocked(t, ctx, d, firstPID)
				if err := barrier.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				var pol, actRes *httptest.ResponseRecorder
				select {
				case pol = <-policyDone:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				select {
				case actRes = <-writeDone:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if pol.Code != 200 {
					t.Fatalf("policy %d %s", pol.Code, pol.Body)
				}
				expected := 201
				if first == "policy" {
					expected = 403
				}
				if actRes.Code != expected {
					t.Fatalf("participation %d want %d: %s", actRes.Code, expected, actRes.Body)
				}
				count := voteCount(t, d, tid)
				eventType := "portal.vote_cast"
				expectedCount := 0
				if first == "participation" {
					expectedCount = 1
				}
				if !isVote {
					count = wishCount(t, d, tid) - 1
					eventType = "portal.wish_submitted"
				}
				if count != expectedCount {
					t.Fatalf("mutations %d want %d", count, expectedCount)
				}
				if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
					var n int
					err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type=$1`, eventType).Scan(&n)
					if n != expectedCount {
						t.Errorf("events %d want %d", n, expectedCount)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func waitProductBlocked(t *testing.T, ctx context.Context, d *dbtest.DB, blocker uint32) uint32 {
	t.Helper()
	for {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		var pid uint32
		if err := d.Admin.QueryRow(ctx, `SELECT coalesce((SELECT pid FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)) ORDER BY pid LIMIT 1),0)`, blocker).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if pid != 0 {
			return pid
		}
		runtime.Gosched()
	}
}

func TestProductProjectLinksRemainIndependent(t *testing.T) {
	f := productFixture(t)
	d := f.d
	tid := makeTenant(t, d, "links", "Links")
	admin := makePerson(t, d, tid, "Admin", "admin")
	p1 := insertNode(t, d, tid, "PPR-1", "portal_product", "One", "one", "published", "", "{}")
	p2 := insertNode(t, d, tid, "PPR-2", "portal_product", "Two", "two", "published", "", "{}")
	configureFixtureProduct(t, d, tid, p1, "one", "legacy", true)
	configureFixtureProduct(t, d, tid, p2, "two", "disabled", true)
	setPortal(t, d, tid, true)
	j1 := insertNode(t, d, tid, "PRJ-1", "project", "Project one", "internal one", "open", "", "{}")
	j2 := insertNode(t, d, tid, "PRJ-2", "project", "Project two", "internal two", "open", "", "{}")
	ip := "203.0.113.180:1"
	first := f.do("PUT", "/api/portal/pace", `{"project_id":"`+j1+`"}`, ip, &admin, nil, nil)
	if first.Code != 200 {
		t.Fatalf("legacy link %d %s", first.Code, first.Body)
	}
	second := f.do("PUT", "/api/portal/products/"+p2+"/pace", `{"project_id":"`+j2+`"}`, ip, &admin, nil, nil)
	if second.Code != 200 {
		t.Fatalf("scoped link %d %s", second.Code, second.Body)
	}
	for _, entry := range []struct{ path, project string }{{"/api/portal/pace", j1}, {"/api/portal/products/" + p1 + "/pace", j1}, {"/api/portal/products/" + p2 + "/pace", j2}} {
		got := f.do("GET", entry.path, "", ip, &admin, nil, nil)
		var out paceAdmin
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &out) != nil || out.ProjectID != entry.project || out.ReleaseHistory {
			t.Fatalf("independent link %s %d %s", entry.path, got.Code, got.Body)
		}
	}
	// The tenant compatibility row remains bound to the original default.
	legacyProject, _, _, found := paceLinkRow(t, d, tid)
	if !found || legacyProject != j1 {
		t.Fatal("another product overwrote the legacy link")
	}
	if rec := f.do("PUT", "/api/portal/products/"+p2+"/settings", `{"revision":1,"slug":"two","published":true,"is_default":true,"participation_policy":"disabled"}`, ip, &admin, nil, nil); rec.Code != 200 {
		t.Fatalf("default %d %s", rec.Code, rec.Body)
	}
	var out paceAdmin
	rec := f.do("GET", "/api/portal/pace", "", ip, &admin, nil, nil)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.ProjectID != j2 {
		t.Fatalf("default project %d %s", rec.Code, rec.Body)
	}
	rec = f.do("PUT", "/api/portal/pace", `{"project_id":null}`, ip, &admin, nil, nil)
	if rec.Code != 200 {
		t.Fatalf("clear default %d %s", rec.Code, rec.Body)
	}
	rec = f.do("GET", "/api/portal/products/"+p1+"/pace", "", ip, &admin, nil, nil)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.ProjectID != j1 {
		t.Fatal("clearing the new default removed the original product's project")
	}
}

func TestProductMigrationPreservesLegacySelectionAndVotes(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	type legacy struct{ tenant, selected, other, wish, project, nested, deleted string }
	fixtures := make([]legacy, 2)
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1138_portal_products.sql" {
			return nil
		}
		for i := range fixtures {
			f := &fixtures[i]
			if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,'Legacy portal') RETURNING id::text`, fmt.Sprintf("legacy-%d", i)).Scan(&f.tenant); err != nil {
				return err
			}
			if err := db.InTenant(dbtest.Seed(t.Context()), d.App, f.tenant, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
					return err
				}
				insert := func(key, kind, state string, parent any, position int, dest *string) error {
					return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,state,parent_id,position)
                        SELECT $1::uuid,$2,id,$2,$3,$4::uuid,$5 FROM node_kinds WHERE slug=$6 RETURNING id::text`, f.tenant, key, state, parent, position, kind).Scan(dest)
				}
				if err := insert("PPR-1", "portal_product", "published", nil, 20, &f.other); err != nil {
					return err
				}
				if err := insert("PPR-2", "portal_product", "published", nil, 10, &f.selected); err != nil {
					return err
				}
				if err := insert("PWS-1", "portal_wish", "published", f.selected, 1, &f.wish); err != nil {
					return err
				}
				if err := insert("PRJ-1", "project", "open", nil, 1, &f.project); err != nil {
					return err
				}
				if err := insert("PPR-3", "portal_product", "published", f.project, -100, &f.nested); err != nil {
					return err
				}
				if err := insert("PPR-4", "portal_product", "published", nil, -200, &f.deleted); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1::uuid`, f.deleted); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO portal_settings(tenant_id,enabled)VALUES($1::uuid,true)`, f.tenant); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO portal_pace(tenant_id,project_node_id,release_history,revision)VALUES($1::uuid,$2::uuid,false,7)`, f.tenant, f.project); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO portal_votes(tenant_id,wish_id,voter_hash,weight)VALUES($1::uuid,$2::uuid,repeat('a',64),3)`, f.tenant, f.wish)
				return err
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		if err := db.InTenant(dbtest.Seed(t.Context()), d.App, f.tenant, func(tx pgx.Tx) error {
			var id, policy, project string
			var n, sum int
			for _, dormant := range []string{f.nested, f.deleted} {
				var published, isDefault bool
				var policy string
				if err := tx.QueryRow(t.Context(), `SELECT published,is_default,participation_policy FROM portal_products WHERE product_id=$1::uuid`, dormant).Scan(&published, &isDefault, &policy); err != nil {
					return fmt.Errorf("dormant product missing from backfill: %w", err)
				}
				if published || isDefault || policy != "disabled" {
					return errors.New("dormant product backfill enabled publication or participation")
				}
			}
			if err := tx.QueryRow(t.Context(), `SELECT product_id::text,participation_policy FROM portal_products WHERE is_default`).Scan(&id, &policy); err != nil {
				return fmt.Errorf("migration default: %w", err)
			}
			if id != f.selected || policy != "legacy" {
				t.Fatalf("migration default %s %s", id, policy)
			}
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM portal_products`).Scan(&n); err != nil {
				return err
			}
			if n != 4 {
				t.Fatalf("tenant product count %d", n)
			}
			if err := tx.QueryRow(t.Context(), `SELECT project_node_id::text FROM portal_product_pace WHERE product_id=$1::uuid AND NOT release_history`, f.selected).Scan(&project); err != nil {
				return fmt.Errorf("migration project: %w", err)
			}
			if project != f.project {
				t.Fatal("migration changed the linked project")
			}
			if err := tx.QueryRow(t.Context(), `SELECT sum(weight) FROM portal_votes WHERE wish_id=$1::uuid AND product_id=$2::uuid`, f.wish, f.selected).Scan(&sum); err != nil {
				return err
			}
			if sum != 3 {
				t.Fatalf("legacy ballots changed %d", sum)
			}
			doc, err := loadPortal(t.Context(), tx)
			if err != nil {
				return err
			}
			if doc.Product == nil || doc.Product.Key != "PPR-2" || len(doc.Wishes) != 1 || doc.Wishes[0].Votes != 3 {
				t.Fatalf("legacy URL projection %+v", doc)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
