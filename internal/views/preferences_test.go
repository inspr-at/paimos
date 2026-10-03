// SPDX-License-Identifier: AGPL-3.0-only

package views

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestPreferencesArePrivateSmallObjectsPerPerson(t *testing.T) {
	db := dbtest.Open(t)
	ctx := t.Context()
	var tenantID, aliceID, bobID, otherTenantID, carolID string
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Admin.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ('prefs-test', 'Prefs') RETURNING id::text`).Scan(&tenantID))
	must(db.Admin.QueryRow(ctx, `INSERT INTO principals (tenant_id, kind, name) VALUES ($1, 'person', 'alice') RETURNING id::text`, tenantID).Scan(&aliceID))
	must(db.Admin.QueryRow(ctx, `INSERT INTO principals (tenant_id, kind, name) VALUES ($1, 'person', 'bob') RETURNING id::text`, tenantID).Scan(&bobID))
	must(db.Admin.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ('prefs-other', 'Other') RETURNING id::text`).Scan(&otherTenantID))
	must(db.Admin.QueryRow(ctx, `INSERT INTO principals (tenant_id, kind, name) VALUES ($1, 'person', 'carol') RETURNING id::text`, otherTenantID).Scan(&carolID))

	mux := http.NewServeMux()
	New(db.App).Mount(mux)
	request := func(tenantID, principalID, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if principalID != "" {
			req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{ID: principalID, TenantID: tenantID}))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	value := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("preference response status=%d body=%s", w.Code, w.Body.String())
		}
		var out preference
		must(json.Unmarshal(w.Body.Bytes(), &out))
		return string(out.Value)
	}

	// Never written reads as null.
	empty := request(tenantID, aliceID, http.MethodGet, "/api/preferences/list:p1", "")
	if empty.Code != http.StatusOK || value(empty) != "null" {
		t.Fatalf("unset status=%d body=%s", empty.Code, empty.Body.String())
	}
	put := request(tenantID, aliceID, http.MethodPut, "/api/preferences/list:p1", `{"value":{"columns":["key","title"],"widths":{"title":420}}}`)
	if put.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", put.Code, put.Body.String())
	}
	read := request(tenantID, aliceID, http.MethodGet, "/api/preferences/list:p1", "")
	if !strings.Contains(value(read), `"title": 420`) && !strings.Contains(value(read), `"title":420`) {
		t.Fatalf("read back = %s", read.Body.String())
	}
	// Overwrite replaces the whole value.
	value(request(tenantID, aliceID, http.MethodPut, "/api/preferences/list:p1", `{"value":{"split":0.4}}`))
	if got := value(request(tenantID, aliceID, http.MethodGet, "/api/preferences/list:p1", "")); strings.Contains(got, "columns") || !strings.Contains(got, "split") {
		t.Fatalf("overwrite = %s", got)
	}
	// Private to the person, and to the tenant.
	if got := value(request(tenantID, bobID, http.MethodGet, "/api/preferences/list:p1", "")); got != "null" {
		t.Fatalf("another person read %s", got)
	}
	if got := value(request(otherTenantID, carolID, http.MethodGet, "/api/preferences/list:p1", "")); got != "null" {
		t.Fatalf("another tenant read %s", got)
	}
	// Validation: key shape, object values only, size cap, unknown fields.
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/api/preferences/Bad%20Key", `{"value":{}}`, http.StatusBadRequest},
		{"/api/preferences/list:p1", `{"value":[1,2]}`, http.StatusBadRequest},
		{"/api/preferences/list:p1", `{"value":"text"}`, http.StatusBadRequest},
		{"/api/preferences/list:p1", `{"value":{},"extra":1}`, http.StatusBadRequest},
		{"/api/preferences/list:p1", `{"value":{"big":"` + strings.Repeat("x", maxPreferenceBytes) + `"}}`, http.StatusBadRequest},
	} {
		if got := request(tenantID, aliceID, http.MethodPut, tc.path, tc.body); got.Code != tc.status && !(tc.status == http.StatusBadRequest && got.Code == http.StatusRequestEntityTooLarge) {
			t.Fatalf("PUT %s %.40s status=%d want %d body=%s", tc.path, tc.body, got.Code, tc.status, got.Body.String())
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		got := request("", "", method, "/api/preferences/list:anonymous", `{"value":{"anonymous":true}}`)
		if got.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s status=%d body=%s", method, got.Code, got.Body.String())
		}
	}
	var anonymousWrites int
	must(db.Admin.QueryRow(ctx, `SELECT count(*) FROM user_preferences WHERE key='list:anonymous'`).Scan(&anonymousWrites))
	if anonymousWrites != 0 {
		t.Fatalf("anonymous PUT persisted %d preferences", anonymousWrites)
	}
	// No events for preferences.
	var events int
	must(db.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1`, tenantID).Scan(&events))
	if events != 0 {
		t.Fatalf("preferences appended %d events", events)
	}
}
