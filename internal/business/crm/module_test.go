// SPDX-License-Identifier: AGPL-3.0-only

package crm

import (
	"bytes"
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
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestBindContactPrincipal(t *testing.T) {
	f := setup(t)
	foreignEvents := count(t, f, f.other.TenantID, `SELECT count(*) FROM events`)
	body := fmt.Sprintf(`{"principal_id":%q}`, f.customer)
	w := request(f.handler, f.admin, "POST", "/api/crm/contacts/"+strings.ToUpper(f.contact)+"/principals", body)
	expect(t, w, 201)
	var first Binding
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.PrincipalID != f.customer || first.ContactNodeID != f.contact || first.BoundByPrincipalID != f.admin.ID || first.BoundAt.IsZero() {
		t.Fatalf("binding %+v", first)
	}
	ev := logEvents(t, f)
	if len(ev) != 1 || ev[0].Type != EventContactBound || ev[0].ActorPrincipalID != f.admin.ID || ev[0].NodeID == nil || *ev[0].NodeID != f.contact || string(ev[0].Before) != "null" {
		t.Fatalf("event %+v", ev)
	}
	var snap Binding
	if err := json.Unmarshal(ev[0].After, &snap); err != nil || snap.PrincipalID != first.PrincipalID || !snap.BoundAt.Equal(first.BoundAt) {
		t.Fatalf("snapshot %+v err %v", snap, err)
	}

	other := f.admin
	other.ID = f.second
	replay := request(f.handler, other, "POST", "/api/crm/contacts/"+f.contact+"/principals", body)
	expect(t, replay, 201)
	var again Binding
	if err := json.Unmarshal(replay.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again.BoundByPrincipalID != f.admin.ID || !again.BoundAt.Equal(first.BoundAt) || len(logEvents(t, f)) != 1 {
		t.Fatalf("replay wrote a new binding or event: %+v", again)
	}
	if count(t, f, f.admin.TenantID, `SELECT count(*) FROM crm_contact_principals`) != 1 {
		t.Fatal("replay inserted a second row")
	}

	kinds, err := f.plugins.NodeKinds(t.Context(), f.admin, ID)
	if err != nil || len(kinds) != 2 || kinds[0].Slug != Contact || kinds[1].Slug != Organisation {
		t.Fatalf("kinds %+v err %v", kinds, err)
	}
	views, err := f.plugins.Views(t.Context(), f.admin, ID)
	if err != nil || len(views) != 1 || views[0].ID != viewID {
		t.Fatalf("views %+v err %v", views, err)
	}
	decision, err := f.plugins.ApplyResult(t.Context(), f.admin, ID, plugins.StepResult{
		StepRequest:    plugins.StepRequest{Operation: OperationBind, Payload: BindFacts{ContactLive: true, ContactKind: Contact, PrincipalKind: string(tenant.Person), ActorKind: string(tenant.Person), ActorAdmin: true}},
		ClaimedOutcome: "bound",
	})
	if err != nil || decision.Proceed || decision.AdvancesRelease || decision.AdvancesAccess || decision.Outcome != "not_authority" {
		t.Fatalf("apply %+v err %v", decision, err)
	}
	if _, err := f.plugins.Evaluate(t.Context(), f.admin, ID, plugins.StepRequest{Operation: OperationBind, Payload: BindFacts{}}); err != plugins.ErrDenied {
		t.Fatalf("evaluate should fail closed without steps.evaluate: %v", err)
	}

	var otherContact string
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.other.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title)
			VALUES ($1,$2,'CON-1','Other contact') RETURNING id::text`, f.other.TenantID, f.otherContactKind).Scan(&otherContact)
	})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.handler, f.other, "POST", "/api/crm/contacts/"+f.contact+"/principals", fmt.Sprintf(`{"principal_id":%q}`, f.otherCustomer)), 404)
	expect(t, request(f.handler, f.other, "POST", "/api/crm/contacts/"+otherContact+"/principals", fmt.Sprintf(`{"principal_id":%q}`, f.otherCustomer)), 201)
	if count(t, f, f.admin.TenantID, `SELECT count(*) FROM crm_contact_principals`) != 1 || count(t, f, f.other.TenantID, `SELECT count(*) FROM crm_contact_principals`) != 1 {
		t.Fatal("binding crossed tenants")
	}
	if count(t, f, f.other.TenantID, `SELECT count(*) FROM events`) != foreignEvents+1 {
		t.Fatal("other tenant event missing or leaked")
	}
}

func TestBindRejectsClosedGatesWithoutEvents(t *testing.T) {
	f := setup(t)
	before := len(logEvents(t, f))
	member := f.admin
	member.ID = f.member
	member.Roles = []string{"member"}
	agent := f.admin
	agent.Kind = tenant.Agent
	agent.Roles = []string{"admin"}
	customerCall := fmt.Sprintf(`{"principal_id":%q}`, f.customer)
	cases := []struct {
		name   string
		caller tenant.Principal
		path   string
		body   string
		status int
	}{
		{"member", member, f.contact, customerCall, 403},
		{"agent", agent, f.contact, customerCall, 403},
		{"anonymous", tenant.Principal{}, f.contact, customerCall, 401},
		{"email", f.admin, f.contact, fmt.Sprintf(`{"principal_id":%q,"email":"ada@example.com"}`, f.customer), 400},
		{"bad contact", f.admin, "not-a-uuid", customerCall, 400},
		{"bad principal", f.admin, f.contact, `{"principal_id":"nope"}`, 400},
		{"missing contact", f.admin, "00000000-0000-4000-8000-000000000099", customerCall, 404},
		{"missing principal", f.admin, f.contact, `{"principal_id":"00000000-0000-4000-8000-000000000099"}`, 404},
		{"task", f.admin, f.task, customerCall, 409},
		{"deleted", f.admin, f.deleted, customerCall, 404},
		{"agent target", f.admin, f.contact, fmt.Sprintf(`{"principal_id":%q}`, f.agent), 409},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, request(f.handler, tc.caller, "POST", "/api/crm/contacts/"+tc.path+"/principals", tc.body), tc.status)
		})
	}
	if len(logEvents(t, f)) != before || count(t, f, f.admin.TenantID, `SELECT count(*) FROM crm_contact_principals`) != 0 {
		t.Fatal("rejected bind wrote a row or event")
	}

	f.setInstall(t, false, f.digest, []string{fence.PermStepsApply, fence.PermNodesContribute, fence.PermViewsProvide})
	expect(t, request(f.handler, f.admin, "POST", "/api/crm/contacts/"+f.contact+"/principals", customerCall), 409)
	f.setInstall(t, true, strings.Repeat("ab", 32), []string{fence.PermStepsApply})
	expect(t, request(f.handler, f.admin, "POST", "/api/crm/contacts/"+f.contact+"/principals", customerCall), 409)
	f.setInstall(t, true, f.digest, []string{fence.PermNodesContribute})
	expect(t, request(f.handler, f.admin, "POST", "/api/crm/contacts/"+f.contact+"/principals", customerCall), 409)
	bare := f.admin
	bare.TenantID = f.bareTenant
	bare.ID = f.bareAdmin
	expect(t, request(f.handler, bare, "POST", "/api/crm/contacts/"+f.contact+"/principals", customerCall), 409)
	if len(logEvents(t, f)) != before || count(t, f, f.admin.TenantID, `SELECT count(*) FROM crm_contact_principals`) != 0 {
		t.Fatal("closed installation wrote a binding")
	}

	f.setInstall(t, true, f.digest, []string{fence.PermStepsApply})
	forged := f.admin
	forged.ID = f.other.ID
	expect(t, request(f.handler, forged, "POST", "/api/crm/contacts/"+f.contact+"/principals", customerCall), 403)
	if count(t, f, f.admin.TenantID, `SELECT count(*) FROM crm_contact_principals`) != 0 || len(logEvents(t, f)) != before {
		t.Fatal("failed event append left a binding")
	}
	expect(t, request(f.handler, f.admin, "POST", "/api/crm/contacts/"+f.contact+"/principals", customerCall), 201)
	if count(t, f, f.admin.TenantID, `SELECT count(*) FROM crm_contact_principals`) != 1 || len(logEvents(t, f)) != before+1 {
		t.Fatal("valid bind after a rolled-back write did not commit once")
	}
}

func TestConcurrentBindIsOneEvent(t *testing.T) {
	f := setup(t)
	body := fmt.Sprintf(`{"principal_id":%q}`, f.customer)
	path := "/api/crm/contacts/" + f.contact + "/principals"
	statuses := make(chan int, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			statuses <- request(f.handler, f.admin, "POST", path, body).Code
		})
	}
	close(start)
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[201] != 2 || count(t, f, f.admin.TenantID, `SELECT count(*) FROM crm_contact_principals`) != 1 || len(logEvents(t, f)) != 1 {
		t.Fatalf("concurrent bind: %v", counts)
	}
}

type fixture struct {
	db               *dbtest.DB
	handler          http.Handler
	plugins          *plugins.Module
	admin, other     tenant.Principal
	digest           string
	contact          string
	task             string
	deleted          string
	customer         string
	second           string
	member           string
	agent            string
	otherCustomer    string
	contactKind      string
	otherContactKind string
	bareTenant       string
	bareAdmin        string
	eventStart       int64
}

func setup(t *testing.T) fixture {
	t.Helper()
	d := dbtest.Open(t)
	plug, err := Plugin()
	if err != nil {
		t.Fatal(err)
	}
	reg := plugins.NewRegistry()
	if err := reg.Register(plug); err != nil {
		t.Fatal(err)
	}
	reg.Seal()
	f := fixture{db: d, digest: plug.Manifest.DigestSHA256, handler: (&httpapi.Server{
		Pool:    d.App,
		Modules: []httpapi.Module{New(d.App, reg), events.New(d.App)},
	}).Handler(), plugins: plugins.NewWithRegistry(d.App, reg)}
	f.admin.Kind = tenant.Person
	f.admin.Roles = []string{"admin"}
	f.other.Kind = tenant.Person
	f.other.Roles = []string{"admin"}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('crm-a','A') RETURNING id::text`).Scan(&f.admin.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('crm-b','B') RETURNING id::text`).Scan(&f.other.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('crm-c','C') RETURNING id::text`).Scan(&f.bareTenant); err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, f.admin.TenantID, func(tx pgx.Tx) error {
		var err error
		f.admin.ID, err = insertPrincipal(t, tx, f.admin.TenantID, "person", "Admin", []string{"admin"})
		if err != nil {
			return err
		}
		f.second, err = insertPrincipal(t, tx, f.admin.TenantID, "person", "Second", []string{"admin"})
		if err != nil {
			return err
		}
		f.member, err = insertPrincipal(t, tx, f.admin.TenantID, "person", "Member", []string{"member"})
		if err != nil {
			return err
		}
		f.customer, err = insertPrincipal(t, tx, f.admin.TenantID, "person", "Customer", []string{"customer"})
		if err != nil {
			return err
		}
		f.agent, err = insertPrincipal(t, tx, f.admin.TenantID, "agent", "Agent", nil)
		if err != nil {
			return err
		}
		f.contactKind, err = insertKind(t, tx, f.admin.TenantID, Contact, "CON")
		if err != nil {
			return err
		}
		if _, err = insertKind(t, tx, f.admin.TenantID, Organisation, "ORG"); err != nil {
			return err
		}
		taskKind, err := kindID(t, tx, f.admin.TenantID, "work")
		if err != nil {
			return err
		}
		f.contact, err = insertNode(t, tx, f.admin.TenantID, f.contactKind, "CON-1", "Ada")
		if err != nil {
			return err
		}
		f.deleted, err = insertNode(t, tx, f.admin.TenantID, f.contactKind, "CON-2", "Gone")
		if err != nil {
			return err
		}
		if _, err = tx.Exec(t.Context(), `UPDATE nodes SET deleted_at = now() WHERE tenant_id = $1 AND id = $2`, f.admin.TenantID, f.deleted); err != nil {
			return err
		}
		f.task, err = insertNode(t, tx, f.admin.TenantID, taskKind, "TSK-1", "Task")
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO plugin_installations
			(tenant_id, plugin_id, version, manifest_digest_sha256, owner, enabled, permissions, updated_by_principal_id)
			VALUES ($1,$2,$3,$4,$5,true,$6,$7)`,
			f.admin.TenantID, ID, Version, f.digest, Owner,
			[]string{fence.PermNodesContribute, fence.PermViewsProvide, fence.PermStepsApply}, f.admin.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, f.other.TenantID, func(tx pgx.Tx) error {
		var err error
		f.other.ID, err = insertPrincipal(t, tx, f.other.TenantID, "person", "Other admin", []string{"admin"})
		if err != nil {
			return err
		}
		f.otherCustomer, err = insertPrincipal(t, tx, f.other.TenantID, "person", "Other customer", []string{"customer"})
		if err != nil {
			return err
		}
		f.otherContactKind, err = insertKind(t, tx, f.other.TenantID, Contact, "CON")
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO plugin_installations
			(tenant_id, plugin_id, version, manifest_digest_sha256, owner, enabled, permissions, updated_by_principal_id)
			VALUES ($1,$2,$3,$4,$5,true,$6,$7)`,
			f.other.TenantID, ID, Version, f.digest, Owner, []string{fence.PermStepsApply}, f.other.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, f.bareTenant, func(tx pgx.Tx) error {
		var err error
		f.bareAdmin, err = insertPrincipal(t, tx, f.bareTenant, "person", "Bare", []string{"admin"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(t.Context(), `SELECT coalesce(max(id),0) FROM events WHERE tenant_id=$1`, f.admin.TenantID).Scan(&f.eventStart); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f fixture) setInstall(t *testing.T, enabled bool, digest string, perms []string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE plugin_installations
			SET enabled = $3, manifest_digest_sha256 = $4, permissions = $5
			WHERE tenant_id = $1 AND plugin_id = $2`, f.admin.TenantID, ID, enabled, digest, perms)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertPrincipal(t *testing.T, tx pgx.Tx, tenantID, kind, name string, roles []string) (string, error) {
	t.Helper()
	if roles == nil {
		roles = []string{}
	}
	var id string
	err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,$2,$3,$4) RETURNING id::text`, tenantID, kind, name, roles).Scan(&id)
	if err == nil {
		err = dbtest.BindLegacyTx(t.Context(), tx, tenantID, id)
	}
	return id, err
}

func insertKind(t *testing.T, tx pgx.Tx, tenantID, slug, prefix string) (string, error) {
	t.Helper()
	var id string
	err := tx.QueryRow(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,$2,$2,$3,$2) RETURNING id::text`, tenantID, slug, prefix).Scan(&id)
	return id, err
}

func kindID(t *testing.T, tx pgx.Tx, tenantID, slug string) (string, error) {
	t.Helper()
	var id string
	err := tx.QueryRow(t.Context(), `SELECT id::text FROM node_kinds WHERE tenant_id=$1 AND slug=$2`, tenantID, slug).Scan(&id)
	return id, err
}

func insertNode(t *testing.T, tx pgx.Tx, tenantID, kindID, key, title string) (string, error) {
	t.Helper()
	var id string
	err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) VALUES($1,$2,$3,$4) RETURNING id::text`, tenantID, kindID, key, title).Scan(&id)
	return id, err
}

func count(t *testing.T, f fixture, tenantID, sql string) int {
	t.Helper()
	var n int
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), sql).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func request(h http.Handler, p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if p.ID != "" {
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d want %d: %s", w.Code, status, w.Body.String())
	}
}

func logEvents(t *testing.T, f fixture) []events.Event {
	t.Helper()
	w := request(f.handler, f.admin, "GET", fmt.Sprintf("/api/events?after=%d", f.eventStart), "")
	expect(t, w, 200)
	var result struct {
		Items []events.Event `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Items
}

func TestBindingJSONOmitsNoExtraAuthority(t *testing.T) {
	raw, err := json.Marshal(Binding{PrincipalID: "p", ContactNodeID: "c", BoundByPrincipalID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("email")) || bytes.Contains(raw, []byte("grant")) {
		t.Fatalf("binding json %s", raw)
	}
}
