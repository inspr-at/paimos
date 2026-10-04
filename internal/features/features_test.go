// SPDX-License-Identifier: AGPL-3.0-only

package features

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func boolean(v bool) *bool { return &v }

func TestEvaluationPrecedence(t *testing.T) {
	for _, test := range []struct {
		name            string
		tenant, project *bool
		enabled         bool
		source          string
	}{
		{"default off", nil, nil, false, "default"},
		{"tenant on", boolean(true), nil, true, "tenant"},
		{"tenant off", boolean(false), nil, false, "tenant"},
		{"project on", nil, boolean(true), true, "project"},
		{"project off", nil, boolean(false), false, "project"},
		{"project off beats tenant on", boolean(true), boolean(false), false, "project"},
		{"project on beats tenant off", boolean(false), boolean(true), true, "project"},
		{"both on", boolean(true), boolean(true), true, "project"},
		{"both off", boolean(false), boolean(false), false, "project"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := evaluate(Catalog()[0], test.tenant, test.project)
			if got.Enabled != test.enabled || got.Source != test.source {
				t.Fatalf("evaluation = %+v", got)
			}
		})
	}
	enabled, err := New(nil).Enabled(t.Context(), tenant.Principal{}, "unknown", "")
	if enabled || err != nil {
		t.Fatal("unknown key did not fail closed without a database")
	}
}

type fixture struct {
	d                                            *dbtest.DB
	s                                            *Service
	mux                                          *http.ServeMux
	admin, member, agent, other, guest, inactive tenant.Principal
	project, hidden, foreign                     string
}

func setup(t *testing.T) fixture {
	t.Helper()
	f := fixture{d: dbtest.Open(t), mux: http.NewServeMux(), project: "99999999-9999-4999-8999-999999999999", hidden: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", foreign: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}
	f.admin = tenant.Principal{TenantID: "11111111-1111-4111-8111-111111111111", ID: "22222222-2222-4222-8222-222222222222", Kind: tenant.Person}
	f.member = tenant.Principal{TenantID: f.admin.TenantID, ID: "33333333-3333-4333-8333-333333333333", Kind: tenant.Person}
	f.agent = tenant.Principal{TenantID: f.admin.TenantID, ID: "44444444-4444-4444-8444-444444444444", Kind: tenant.Agent, Scopes: []string{"nodes.read", "settings.manage"}}
	f.other = tenant.Principal{TenantID: "55555555-5555-4555-8555-555555555555", ID: "66666666-6666-4666-8666-666666666666", Kind: tenant.Person}
	f.guest = tenant.Principal{TenantID: f.admin.TenantID, ID: "77777777-7777-4777-8777-777777777777", Kind: tenant.Person}
	f.inactive = tenant.Principal{TenantID: f.admin.TenantID, ID: "88888888-8888-4888-8888-888888888888", Kind: tenant.Person}
	for _, p := range []tenant.Principal{f.admin, f.other} {
		if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, p.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,$2,'Flags')`, p.TenantID, "flags-"+p.TenantID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []tenant.Principal{f.admin, f.member, f.agent, f.other, f.guest, f.inactive} {
		if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, p.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,$3,'Flag fixture')`, p.TenantID, p.ID, p.Kind)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if p.ID != f.guest.ID {
			role := "admin"
			if p.ID == f.member.ID {
				role = "member"
			}
			dbtest.BindRole(t, f.d, p.TenantID, p.ID, role)
		}
	}
	for _, item := range []struct {
		p       tenant.Principal
		id, key string
	}{{f.admin, f.project, "FLAG-1"}, {f.admin, f.hidden, "HIDDEN-1"}, {f.other, f.foreign, "FOREIGN-1"}} {
		if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, item.p.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title,state)
                SELECT $1,$2,$3,id,'Flag project','active' FROM node_kinds WHERE tenant_id=$1 AND slug='project'`, item.p.TenantID, item.id, item.key)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
        SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, f.admin.TenantID, f.guest.ID, f.project); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE principals SET status='deactivated' WHERE id=$1`, f.inactive.ID); err != nil {
		t.Fatal(err)
	}
	f.s = New(f.d.App)
	f.s.Mount(f.mux)
	return f
}

func (f fixture) call(p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(tenant.WithPrincipal(context.Background(), p))
	recorder := httptest.NewRecorder()
	f.mux.ServeHTTP(recorder, request)
	return recorder
}

func status(t *testing.T, r *httptest.ResponseRecorder, want int) {
	t.Helper()
	if r.Code != want {
		t.Fatalf("status %d want %d: %s", r.Code, want, r.Body.String())
	}
	if r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("flags can be cached")
	}
}

func saved(t *testing.T, r *httptest.ResponseRecorder) Saved {
	t.Helper()
	status(t, r, 200)
	var result Saved
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func (f fixture) setting(t *testing.T, p tenant.Principal, project string) Setting {
	t.Helper()
	items, err := f.s.Settings(t.Context(), p, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("settings = %v", items)
	}
	return items[0]
}

func TestDarkReleaseOverridesRevisionsAndAudit(t *testing.T) {
	f := setup(t)
	before := f.setting(t, f.admin, "")
	if before.Enabled || before.Source != "default" || before.Revision != 0 || before.Override != nil {
		t.Fatalf("fresh flags = %+v", before)
	}
	path := "/api/settings/features/" + WorkspaceSummary
	enabled := saved(t, f.call(f.admin, "PUT", path, `{"enabled":true,"expected_revision":0}`))
	if !enabled.Feature.Enabled || enabled.Feature.Revision != 1 || enabled.EventID == nil {
		t.Fatalf("enable = %+v", enabled)
	}
	// A separate instance sees it immediately: no restart, deployment or cache.
	on, err := New(f.d.App).Enabled(t.Context(), f.member, WorkspaceSummary, "")
	if err != nil || !on {
		t.Fatalf("dark feature did not release: %v %v", on, err)
	}
	evalResponse := f.call(f.member, "GET", "/api/features", "")
	status(t, evalResponse, 200)
	if strings.Contains(evalResponse.Body.String(), "override") || strings.Contains(evalResponse.Body.String(), "revision") {
		t.Fatal("evaluation exposes admin state")
	}
	unchanged := saved(t, f.call(f.admin, "PUT", path, `{"enabled":true,"expected_revision":1}`))
	if unchanged.EventID != nil || unchanged.Feature.Revision != 1 {
		t.Fatal("identical write created an audit event")
	}
	status(t, f.call(f.admin, "PUT", path, `{"enabled":false,"expected_revision":0}`), 409)
	projectPath := path + "?project_id=" + f.project
	off := saved(t, f.call(f.admin, "PUT", projectPath, `{"enabled":false,"expected_revision":0}`))
	if off.Feature.Enabled || off.Feature.Source != "project" {
		t.Fatal("explicit project OFF lost to tenant ON")
	}
	if f.setting(t, f.admin, f.hidden).Source != "tenant" {
		t.Fatal("project override affected a sibling")
	}
	if f.setting(t, f.other, "").Enabled {
		t.Fatal("tenant override leaked")
	}
	inherited := saved(t, f.call(f.admin, "PUT", projectPath, `{"enabled":null,"expected_revision":1}`))
	if !inherited.Feature.Enabled || inherited.Feature.Source != "tenant" || inherited.Feature.Revision != 2 || inherited.Feature.Override != nil {
		t.Fatalf("reset = %+v", inherited)
	}
	status(t, f.call(f.admin, "PUT", projectPath, `{"enabled":true,"expected_revision":0}`), 409)
	saved(t, f.call(f.admin, "PUT", path, `{"enabled":false,"expected_revision":1}`))
	projectOn := saved(t, f.call(f.admin, "PUT", projectPath, `{"enabled":true,"expected_revision":2}`))
	if !projectOn.Feature.Enabled || projectOn.Feature.Source != "project" {
		t.Fatal("project ON lost to tenant OFF")
	}
	reset := saved(t, f.call(f.admin, "PUT", path, `{"enabled":null,"expected_revision":2}`))
	if reset.Feature.Enabled || reset.Feature.Source != "default" || reset.Feature.Revision != 3 {
		t.Fatal("tenant reset did not restore OFF")
	}
	var actor, typ string
	var auditBefore, auditAfter snapshot
	var rawBefore, rawAfter []byte
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT actor_principal_id::text,type,before,after FROM events WHERE tenant_id=$1 AND id=$2`, f.admin.TenantID, *off.EventID).Scan(&actor, &typ, &rawBefore, &rawAfter); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawBefore, &auditBefore); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawAfter, &auditAfter); err != nil {
		t.Fatal(err)
	}
	if actor != f.admin.ID || typ != "feature.updated" || auditBefore.Revision != 0 || auditBefore.Override != nil || auditAfter.Override == nil || *auditAfter.Override || auditAfter.ProjectID == nil || *auditAfter.ProjectID != f.project {
		t.Fatal("audit lacks actor and scoped before/after")
	}
	var count int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='feature.updated'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Fatalf("audit count = %d, want 6", count)
	}
}

func TestAuthorizationProjectVisibilityAndRLS(t *testing.T) {
	f := setup(t)
	path := "/api/settings/features/" + WorkspaceSummary
	for _, p := range []tenant.Principal{f.member, f.agent, f.guest, f.inactive} {
		status(t, f.call(p, "GET", "/api/settings/features", ""), 403)
		status(t, f.call(p, "PUT", path, `{"enabled":true,"expected_revision":0}`), 403)
	}
	status(t, f.call(tenant.Principal{}, "GET", "/api/features", ""), 401)
	status(t, f.call(tenant.Principal{}, "PUT", path, `{"enabled":true,"expected_revision":0}`), 401)
	status(t, f.call(f.inactive, "GET", "/api/features", ""), 403)
	status(t, f.call(f.agent, "GET", "/api/features", ""), 200)
	limited := f.agent
	limited.Scopes = nil
	status(t, f.call(limited, "GET", "/api/features", ""), 403)
	status(t, f.call(f.guest, "GET", "/api/features", ""), 200)
	status(t, f.call(f.guest, "GET", "/api/features?project_id="+f.project, ""), 200)
	status(t, f.call(f.guest, "GET", "/api/features?project_id="+f.hidden, ""), 403)
	status(t, f.call(f.admin, "GET", "/api/settings/features?project_id="+f.foreign, ""), 404)
	status(t, f.call(f.admin, "PUT", path+"?project_id="+f.foreign, `{"enabled":true,"expected_revision":0}`), 404)
	saved(t, f.call(f.admin, "PUT", path, `{"enabled":true,"expected_revision":0}`))
	saved(t, f.call(f.admin, "PUT", path+"?project_id="+f.hidden, `{"enabled":true,"expected_revision":0}`))
	// The restricted app role enforces isolation even without handler filters.
	err := db.InTenant(tenant.WithPrincipal(t.Context(), f.guest), f.d.App, f.guest.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM features`).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Errorf("guest sees %d rows, want only tenant baseline", count)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(tenant.WithPrincipal(t.Context(), f.other), f.d.App, f.other.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM features`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Errorf("other tenant sees %d flags", count)
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO features(tenant_id,key,enabled) VALUES($1,'foreign-write',true)`, f.admin.TenantID)
		if err == nil {
			t.Error("cross-tenant write passed RLS")
		}
		return errors.New("rollback forbidden write probe")
	})
	if err == nil {
		t.Fatal("probe should roll back")
	}
	var count int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='feature.updated'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("denied mutation left audit: %d", count)
	}
}

func TestValidationConcurrencyAndAtomicAudit(t *testing.T) {
	f := setup(t)
	path := "/api/settings/features/" + WorkspaceSummary
	for _, body := range []string{`{}`, `{"enabled":true}`, `{"expected_revision":0}`, `{"enabled":"on","expected_revision":0}`, `{"enabled":1,"expected_revision":0}`, `{"enabled":true,"expected_revision":-1}`, `{"enabled":false,"expected_revision":0,"tenant_id":"other"}`, `{"enabled":true,"expected_revision":0} {}`} {
		status(t, f.call(f.admin, "PUT", path, body), 400)
	}
	status(t, f.call(f.admin, "GET", "/api/features?project_id=invalid", ""), 400)
	status(t, f.call(f.admin, "PUT", path+"?project_id=invalid", `{"enabled":true,"expected_revision":0}`), 400)
	status(t, f.call(f.admin, "PUT", "/api/settings/features/unshipped", `{"enabled":true,"expected_revision":0}`), 404)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, value := range []bool{true, false} {
		wg.Add(1)
		go func(value bool) {
			defer wg.Done()
			_, err := f.s.Save(t.Context(), f.admin, WorkspaceSummary, "", Write{boolean(value), 0})
			results <- err
		}(value)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		var f failure
		if err == nil {
			success++
		} else if errors.As(err, &f) && f.status == 409 {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent create: success=%d conflict=%d", success, conflict)
	}
	// Fail the event insert deliberately. The override must roll back too.
	if _, err := f.d.Admin.Exec(t.Context(), `CREATE FUNCTION reject_flag_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test audit failure'; END; $$;
        CREATE TRIGGER reject_flag_audit BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_flag_audit()`); err != nil {
		t.Fatal(err)
	}
	before := f.setting(t, f.admin, "")
	_, err := f.s.Save(t.Context(), f.admin, WorkspaceSummary, "", Write{boolean(!before.Enabled), before.Revision})
	if err == nil {
		t.Fatal("write succeeded without audit")
	}
	after := f.setting(t, f.admin, "")
	if before.Enabled != after.Enabled || before.Revision != after.Revision {
		t.Fatal("audit failure committed the override")
	}
}
