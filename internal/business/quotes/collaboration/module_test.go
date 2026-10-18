// SPDX-License-Identifier: AGPL-3.0-only
package collaboration

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/business/quotes"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

type fixture struct {
	db                                            *dbtest.DB
	mux                                           *http.ServeMux
	module                                        *Module
	tenant, quote, admin, other, viewer, customer string
}

func setup(t *testing.T) fixture {
	t.Helper()
	f := fixture{db: dbtest.Open(t), tenant: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
	reg := plugins.NewRegistry()
	for _, id := range []string{"business_crm", "business_costs"} {
		p := plugins.Plugin{Manifest: plugins.Manifest{ID: id, Version: "1", Owner: "aeon", Permissions: []string{fence.PermNodesContribute}}}
		digest, err := plugins.Digest(p)
		if err != nil {
			t.Fatal(err)
		}
		p.Manifest.DigestSHA256 = digest
		if err = reg.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	p, err := quotes.ManifestPlugin()
	if err != nil {
		t.Fatal(err)
	}
	if err = reg.Register(p); err != nil {
		t.Fatal(err)
	}
	reg.Seal()
	ctx := context.Background()
	err = db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `INSERT INTO tenants(id,slug,name) VALUES($1::uuid,'collaboration-test','Collaboration test')`, f.tenant); e != nil {
			return e
		}
		for _, person := range []struct {
			name, role string
			dest       *string
		}{{"admin", "admin", &f.admin}, {"other", "member", &f.other}, {"viewer", "viewer", &f.viewer}, {"customer", "customer", &f.customer}} {
			if e := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person',$2,ARRAY[$3]::text[]) RETURNING id::text`, f.tenant, person.name, person.role).Scan(person.dest); e != nil {
				return e
			}
			if person.role == "viewer" {
				if _, e := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1::uuid,$2::uuid,id,'workspace' FROM roles WHERE tenant_id=$1::uuid AND key='viewer'`, f.tenant, *person.dest); e != nil {
					return e
				}
			} else if e := dbtest.BindLegacyTx(ctx, tx, f.tenant, *person.dest); e != nil {
				return e
			}
		}
		for _, id := range []string{"business_crm", "business_costs", "business_quotes"} {
			plug, _ := reg.Lookup(id)
			if _, e := tx.Exec(ctx, `INSERT INTO plugin_installations(tenant_id,plugin_id,version,manifest_digest_sha256,owner,enabled,permissions,updated_by_principal_id) VALUES($1::uuid,$2,$3,$4,$5,true,$6,$7::uuid)`, f.tenant, id, plug.Manifest.Version, plug.Manifest.DigestSHA256, plug.Manifest.Owner, plug.Manifest.Permissions, f.admin); e != nil {
				return e
			}
		}
		for _, slug := range []string{"organisation", "quote"} {
			if _, e := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1::uuid,$2,$2,upper(left($2,3)),$2)`, f.tenant, slug); e != nil {
				return e
			}
		}
		var org string
		if e := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1::uuid,'ORG-1',id,'Customer' FROM node_kinds WHERE slug='organisation' RETURNING id::text`, f.tenant).Scan(&org); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1::uuid,'QUO-1',id,'Quote' FROM node_kinds WHERE slug='quote' RETURNING id::text`, f.tenant).Scan(&f.quote); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO business_quotes(tenant_id,quote_node_id,customer_org_node_id) VALUES($1::uuid,$2::uuid,$3::uuid)`, f.tenant, f.quote, org); e != nil {
			return e
		}
		const doc = `{"schema_version":1,"minimum_writer_version":1,"sections":[{"id":"11111111-1111-4111-8111-111111111111","nodes":[{"id":"22222222-2222-4222-8222-222222222222","text":"A😀B"}]}],"positions":[]}`
		_, e := tx.Exec(ctx, `INSERT INTO quote_drafts(tenant_id,quote_node_id,document,schema_version,minimum_writer_version,updated_by_principal_id) VALUES($1::uuid,$2::uuid,$3::jsonb,1,1,$4::uuid)`, f.tenant, f.quote, doc, f.admin)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	module, err := New(f.db.App, reg)
	if err != nil {
		t.Fatal(err)
	}
	f.module = module.(*Module)
	f.mux = http.NewServeMux()
	module.Mount(f.mux)
	return f
}
func (f fixture) call(t *testing.T, actor, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{ID: actor, TenantID: f.tenant, Kind: tenant.Person, Roles: []string{"admin"}}))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}
func TestLeasesAndAuthorization(t *testing.T) {
	f := setup(t)
	base := fmt.Sprintf("/api/quotes/%s/presence", f.quote)
	if code, _ := f.call(t, f.customer, "GET", base, ""); code != 403 {
		t.Fatalf("customer presence %d", code)
	}
	if code, _ := f.call(t, f.viewer, "POST", base, `{"mode":"editing","observed_revision":1}`); code != 403 {
		t.Fatalf("viewer editing %d", code)
	}
	code, joined := f.call(t, f.admin, "POST", base, `{"mode":"editing","observed_revision":1,"anchor":{"section_id":"11111111-1111-4111-8111-111111111111","observed_revision":1,"node_id":"22222222-2222-4222-8222-222222222222","text_sha256":"wrong","anchor":1,"focus":2,"fidelity":"precise"}}`)
	if code != 201 {
		t.Fatalf("join %d %v", code, joined)
	}
	session := joined["session_id"].(string)
	code, snapshot := f.call(t, f.other, "GET", base, "")
	if code != 200 {
		t.Fatalf("list %d", code)
	}
	sessions := snapshot["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions %v", sessions)
	}
	// No picture yet: the avatar is not asked for (U27).
	if has, ok := sessions[0].(map[string]any)["has_avatar"].(bool); !ok || has {
		t.Fatalf("has_avatar %v", sessions[0])
	}
	anchor := sessions[0].(map[string]any)["anchor"].(map[string]any)
	if anchor["fidelity"] != "section" {
		t.Fatalf("unverified cursor stayed precise: %v", anchor)
	}
	if code, _ := f.call(t, f.other, "PATCH", base+"/"+session, `{"mode":"editing","observed_revision":1}`); code != 403 {
		t.Fatalf("foreign session update %d", code)
	}
	if code, _ := f.call(t, f.other, "POST", base, fmt.Sprintf(`{"mode":"viewing","observed_revision":1,"resume_session_id":%q}`, session)); code != 403 {
		t.Fatalf("foreign resume %d", code)
	}
	if code, _ := f.call(t, f.admin, "POST", base, fmt.Sprintf(`{"mode":"editing","observed_revision":1,"resume_session_id":%q}`, session)); code != 201 {
		t.Fatalf("own resume %d", code)
	}
	sum := sha256.Sum256([]byte("A😀B"))
	precise := fmt.Sprintf(`{"mode":"editing","observed_revision":1,"resume_session_id":%q,"anchor":{"section_id":"11111111-1111-4111-8111-111111111111","node_id":"22222222-2222-4222-8222-222222222222","observed_revision":1,"text_sha256":"%x","anchor":1,"focus":3,"fidelity":"precise"}}`, session, sum)
	if code, out := f.call(t, f.admin, "POST", base, precise); code != 201 || out["snapshot"].(map[string]any)["sessions"].([]any)[0].(map[string]any)["anchor"].(map[string]any)["fidelity"] != "precise" {
		t.Fatalf("verified caret %d %v", code, out)
	}
	// Freeze the heartbeat clock at the committed join timestamp; runner load
	// must not decide whether the next request crosses the 200 ms limit.
	var lastSeen time.Time
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT last_seen FROM quote_presence WHERE session_id=$1::uuid`, session).Scan(&lastSeen)
	}); err != nil {
		t.Fatal(err)
	}
	f.module.now = func() time.Time { return lastSeen.Add(200*time.Millisecond - time.Nanosecond) }
	if code, _ := f.call(t, f.admin, "PATCH", base+"/"+session, `{"mode":"editing","observed_revision":1}`); code != 429 {
		t.Fatalf("unbounded cursor update %d", code)
	}
	f.module.now = func() time.Time { return lastSeen.Add(200 * time.Millisecond) }
	if code, out := f.call(t, f.admin, "PATCH", base+"/"+session, strings.Replace(precise, fmt.Sprintf(`,"resume_session_id":%q`, session), "", 1)); code != 200 {
		t.Fatalf("cursor update at rate boundary %d %v", code, out)
	}
	f.module.now = time.Now
	ctx := context.Background()
	err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE quote_presence SET last_seen=clock_timestamp()-interval '1 second',last_interaction=clock_timestamp()-interval '61 seconds' WHERE session_id=$1::uuid`, session)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := fmt.Sprintf(`{"mode":"editing","observed_revision":1,"anchor":{"section_id":"11111111-1111-4111-8111-111111111111","node_id":"22222222-2222-4222-8222-222222222222","observed_revision":1,"text_sha256":"%x","anchor":1,"focus":3,"fidelity":"precise"}}`, sum)
	if code, out := f.call(t, f.admin, "PATCH", base+"/"+session, heartbeat); code != 200 || out["sessions"].([]any)[0].(map[string]any)["mode"] != "idle" {
		t.Fatalf("inactive editor did not idle %d %v", code, out)
	}
	err = db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE quote_presence SET expires_at=clock_timestamp()-interval '1 second' WHERE session_id=$1::uuid`, session)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, s := f.call(t, f.other, "GET", base, ""); code != 200 || len(s["sessions"].([]any)) != 0 {
		t.Fatalf("expired ghost %d %v", code, s)
	}
	if code, _ := f.call(t, f.admin, "PATCH", base+"/"+session, `{"mode":"viewing","observed_revision":1}`); code != 404 {
		t.Fatalf("expired update %d", code)
	}
	if code, _ := f.call(t, f.viewer, "POST", base, `{"mode":"viewing","observed_revision":1}`); code != 201 {
		t.Fatalf("viewer view %d", code)
	}
	err = db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1::uuid AND key='customer') WHERE principal_id=$2::uuid AND scope_type='workspace'`, f.tenant, f.viewer)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := f.call(t, f.viewer, "GET", base, ""); code != 403 {
		t.Fatalf("revoked viewer %d", code)
	}
	err = db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE plugin_installations SET enabled=false WHERE plugin_id='business_quotes'`)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := f.call(t, f.admin, "GET", base, ""); code != 403 {
		t.Fatalf("disabled plugin %d", code)
	}
}
func TestConcurrentJoinsAndTenantIsolation(t *testing.T) {
	f := setup(t)
	base := fmt.Sprintf("/api/quotes/%s/presence", f.quote)
	var wg sync.WaitGroup
	codes := make(chan int, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := f.call(t, f.admin, "POST", base, `{"mode":"viewing","observed_revision":1}`)
			codes <- code
		}()
	}
	wg.Wait()
	close(codes)
	joined, limited := 0, 0
	for code := range codes {
		if code == 201 {
			joined++
		} else if code == 429 {
			limited++
		} else {
			t.Fatalf("join status %d", code)
		}
	}
	if joined != 8 || limited != 4 {
		t.Fatalf("session cap: joined=%d limited=%d", joined, limited)
	}
	otherTenant := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	ctx := context.Background()
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, otherTenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO tenants(id,slug,name) VALUES($1::uuid,'other-collab','Other')`, otherTenant)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", base, nil)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{ID: f.admin, TenantID: otherTenant, Kind: tenant.Person, Roles: []string{"admin"}}))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code == 200 {
		t.Fatal("cross-tenant presence leaked")
	}
}

// Stream tests cover the wire's role gate before a long-lived connection.
func TestStreamPermissionGate(t *testing.T) {
	f := setup(t)
	path := fmt.Sprintf("/api/quotes/%s/collaboration/stream", f.quote)
	code, _ := f.call(t, f.customer, "GET", path, "")
	if code != 403 {
		t.Fatalf("customer stream %d", code)
	}
}

func TestStreamScopedNoticeAndRevocation(t *testing.T) {
	f := setup(t)
	actor := tenant.Principal{ID: f.admin, TenantID: f.tenant, Kind: tenant.Person, Roles: []string{"admin"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), actor)))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+fmt.Sprintf("/api/quotes/%s/collaboration/stream", f.quote), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("stream %d", response.StatusCode)
	}
	reader := bufio.NewReader(response.Body)
	readUntil := func(want string) string {
		t.Helper()
		var all strings.Builder
		for !strings.Contains(all.String(), want) {
			line, e := reader.ReadString('\n')
			if e != nil {
				t.Fatalf("stream before %s: %v %s", want, e, all.String())
			}
			all.WriteString(line)
		}
		return all.String()
	}
	if got := readUntil("event: presence"); strings.Contains(got, "Secret test text") {
		t.Fatal("presence exposed document")
	}
	err = db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
		_, e := events.Append(ctx, tx, actor, events.Change{NodeID: &f.quote, Type: "quote.draft_updated", After: map[string]any{"draft_revision": 2, "quote_revision": 2, "client_session_id": "11111111-1111-4111-8111-111111111111", "mutation_id": "22222222-2222-4222-8222-222222222222", "secret": "Secret test text"}})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	general := http.NewServeMux()
	events.New(f.db.App).Mount(general)
	leakReq := httptest.NewRequest("GET", "/api/events?node_id="+f.quote, nil)
	leakReq = leakReq.WithContext(tenant.WithPrincipal(leakReq.Context(), tenant.Principal{ID: f.customer, TenantID: f.tenant, Kind: tenant.Person, Roles: []string{"customer"}}))
	leakRec := httptest.NewRecorder()
	general.ServeHTTP(leakRec, leakReq)
	if leakRec.Code != 200 || strings.Contains(leakRec.Body.String(), "quote.draft_updated") || strings.Contains(leakRec.Body.String(), "Secret test text") {
		t.Fatalf("general feed leaked quote event %d %s", leakRec.Code, leakRec.Body.String())
	}
	got := readUntil("event: quote_change")
	got += readUntil("\n\n")
	if strings.Contains(got, "Secret test text") || !strings.Contains(got, `"draft_revision":2`) || !strings.Contains(got, f.quote) {
		t.Fatalf("unsafe or missing scoped notice %s", got)
	}
	err = db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1::uuid AND key='customer') WHERE principal_id=$2::uuid AND scope_type='workspace'`, f.tenant, f.admin)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := readUntil("event: access_revoked"); !strings.Contains(got, "access_revoked") {
		t.Fatalf("revocation missing %s", got)
	}
}
