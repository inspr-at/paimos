// SPDX-License-Identifier: AGPL-3.0-only
package crm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5/pgconn"
)

type auditProvider struct {
	search func(context.Context) ([]RemoteCustomer, error)
	fetch  func(context.Context) (RemoteCustomer, error)
}

func (p auditProvider) Search(ctx context.Context, _, _ string) ([]RemoteCustomer, error) {
	if p.search != nil {
		return p.search(ctx)
	}
	return nil, nil
}
func (p auditProvider) Fetch(ctx context.Context, _, _ string) (RemoteCustomer, error) {
	return p.fetch(ctx)
}

func auditProviders(t *testing.T, providers map[string]Provider) fixture {
	t.Helper()
	f := setup(t)
	plug, err := PluginWithProviders(providers)
	if err != nil {
		t.Fatal(err)
	}
	reg := plugins.NewRegistry()
	if err := reg.Register(plug); err != nil {
		t.Fatal(err)
	}
	reg.Seal()
	f.handler = (&httpapi.Server{Pool: f.db.App, Modules: []httpapi.Module{NewWithProviders(f.db.App, reg, providers)}}).Handler()
	f.setInstall(t, true, f.digest, []string{fence.PermNodesContribute, fence.PermViewsProvide, fence.PermStepsApply, fence.PermIntegrationsCall})
	for id := range providers {
		expect(t, jsonRequest(t, f, f.admin, "PUT", "/api/crm/providers/"+id+"/config", map[string]any{"enabled": true, "secret_ref": "secret://test/crm"}), 200)
	}
	return f
}

func TestAEON587ProviderSearchReportsAllFailedAndPartial(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "all failed", true: "partial"}[partial], func(t *testing.T) {
			providers := map[string]Provider{"unavailable": auditProvider{search: func(context.Context) ([]RemoteCustomer, error) { return nil, errors.New("private provider detail") }}}
			if partial {
				providers["available"] = auditProvider{search: func(context.Context) ([]RemoteCustomer, error) {
					return []RemoteCustomer{{ExternalID: "one", Name: "Customer"}}, nil
				}}
			}
			f := auditProviders(t, providers)
			w := request(f.handler, f.admin, "GET", "/api/crm/providers/search?q=Customer", "")
			want := 503
			if partial {
				want = 200
			}
			expect(t, w, want)
			if w.Header().Get("X-CRM-Partial-Results") != "true" || !strings.Contains(w.Header().Get("X-CRM-Provider-Availability"), `"state":"unavailable"`) {
				t.Fatal("provider outage was hidden")
			}
			if partial && (!strings.HasPrefix(w.Body.String(), "[") || !strings.Contains(w.Body.String(), "one")) {
				t.Fatal("successful array response changed")
			}
			if strings.Contains(w.Body.String()+w.Header().Get("X-CRM-Provider-Availability"), "private provider detail") {
				t.Fatal("provider detail escaped")
			}
		})
	}
}

func TestAEON587SyncFailurePersistsAfterFetchCancellation(t *testing.T) {
	var cancel context.CancelFunc
	provider := auditProvider{fetch: func(context.Context) (RemoteCustomer, error) {
		if cancel != nil {
			cancel()
			return RemoteCustomer{}, errors.New("private provider detail")
		}
		return RemoteCustomer{Name: "Customer"}, nil
	}}
	f := auditProviders(t, map[string]Provider{"test": provider})
	w := jsonRequest(t, f, f.admin, "POST", "/api/crm/providers/test/import", map[string]any{"external_id": "one"})
	expect(t, w, 201)
	var customer Customer
	if err := json.Unmarshal(w.Body.Bytes(), &customer); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	cancel = stop
	r := httptest.NewRequest("POST", "/api/crm/organisations/"+customer.ID+"/sync", nil).WithContext(tenant.WithPrincipal(ctx, f.admin))
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	expect(t, w, 409)
	status := request(f.handler, f.admin, "GET", "/api/crm/organisations/"+customer.ID+"/sync-status", "")
	expect(t, status, 200)
	if !strings.Contains(status.Body.String(), `"state":"error"`) {
		t.Fatalf("failure status disappeared: %s", status.Body.String())
	}
	if count(t, f, f.admin.TenantID, `SELECT count(*) FROM events WHERE type='crm.customer_sync_failed'`) != 1 {
		t.Fatal("failure audit disappeared")
	}
}

func TestAEON587SyncFailurePersistenceErrorIsVisible(t *testing.T) {
	fail := false
	f := auditProviders(t, map[string]Provider{"test": auditProvider{fetch: func(context.Context) (RemoteCustomer, error) {
		if fail {
			return RemoteCustomer{}, errors.New("private provider detail")
		}
		return RemoteCustomer{Name: "Customer"}, nil
	}}})
	w := jsonRequest(t, f, f.admin, "POST", "/api/crm/providers/test/import", map[string]any{"external_id": "one"})
	expect(t, w, 201)
	var customer Customer
	if err := json.Unmarshal(w.Body.Bytes(), &customer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `CREATE FUNCTION audit_reject_sync_status() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic write failure'; END $$; CREATE TRIGGER audit_reject_sync_status BEFORE INSERT ON crm_provider_sync_status FOR EACH ROW EXECUTE FUNCTION audit_reject_sync_status()`); err != nil {
		t.Fatal(err)
	}
	fail = true
	w = request(f.handler, f.admin, "POST", "/api/crm/organisations/"+customer.ID+"/sync", "")
	expect(t, w, 503)
	if !strings.Contains(w.Body.String(), "sync_failure_unrecorded") || strings.Contains(w.Body.String(), "synthetic write failure") || strings.Contains(w.Body.String(), "private provider detail") {
		t.Fatal("unsafe or missing failure summary")
	}
	if count(t, f, f.admin.TenantID, `SELECT count(*) FROM events WHERE type='crm.customer_sync_failed'`) != 0 {
		t.Fatal("partial failure write committed")
	}
}

func TestAEON587ExternalIdentityUniqueConflictIsSpecific(t *testing.T) {
	for _, name := range []string{"crm_external_identity_unique", "unrelated_unique"} {
		w := httptest.NewRecorder()
		writeErr(w, &pgconn.PgError{Code: "23505", ConstraintName: name})
		want := 500
		if name == "crm_external_identity_unique" {
			want = 409
		}
		if w.Code != want {
			t.Fatalf("constraint %s returned %d", name, w.Code)
		}
		if want == 409 && !strings.Contains(w.Body.String(), "external_identity_conflict") {
			t.Fatal("duplicate provider identity reported an unrelated conflict")
		}
	}
}

func TestAEON587ConcurrentProviderImportReturnsOneConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	arrivals := make(chan struct{}, 2)
	release := make(chan struct{})
	provider := auditProvider{fetch: func(ctx context.Context) (RemoteCustomer, error) {
		arrivals <- struct{}{}
		select {
		case <-release:
			return RemoteCustomer{Name: "Customer"}, nil
		case <-ctx.Done():
			return RemoteCustomer{}, ctx.Err()
		}
	}}
	f := auditProviders(t, map[string]Provider{"test": provider})
	results := make(chan int, 2)
	for range 2 {
		go func() {
			r := httptest.NewRequest(http.MethodPost, "/api/crm/providers/test/import", strings.NewReader(`{"external_id":"one"}`)).WithContext(tenant.WithPrincipal(ctx, f.admin))
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			results <- w.Code
		}()
	}
	for range 2 {
		select {
		case <-arrivals:
		case <-ctx.Done():
			close(release)
			t.Fatal(ctx.Err())
		}
	}
	close(release)
	counts := map[int]int{}
	for range 2 {
		select {
		case code := <-results:
			counts[code]++
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if counts[201] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent imports %v", counts)
	}
	if count(t, f, f.admin.TenantID, `SELECT count(*) FROM nodes WHERE fields->>'external_provider'='test' AND fields->>'external_id'='one'`) != 1 {
		t.Fatal("duplicate external customer")
	}
}
