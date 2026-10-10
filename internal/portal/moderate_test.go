// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestPortalModerationAuthorization(t *testing.T) {
	d := dbtest.Open(t)
	m := New(d.App, false, bytes.Repeat([]byte{11}, 32))
	mux := http.NewServeMux()
	m.Mount(mux)
	f := &fixture{t: t, m: m, d: d, h: mux}

	tenantA := makeTenant(t, d, "mod-a", "Moderation")
	admin := makePerson(t, d, tenantA, "Ada Admin", "admin")
	member := makePerson(t, d, tenantA, "Mina Member", "member")
	agent := makeAgent(t, d, tenantA, "Portal agent", "admin", []string{"nodes.write", "settings.manage"})
	product := insertNode(t, d, tenantA, "PPR-1", "portal_product", "Harbour catalog", "Ready in the morning.", "published", "", "{}")
	feature := insertNode(t, d, tenantA, "PCF-1", "portal_feature", "Deadline radar", "A public summary", "planned", product, `{"internal_note":"SECRET-NOTE"}`)
	wish := insertNode(t, d, tenantA, "PWS-1", "portal_wish", "Quiet wish", "A bell before opening.", "pending", product, "{}")
	other := insertNode(t, d, tenantA, "PWS-2", "portal_wish", "Noisy wish", "Leave this one out.", "pending", product, "{}")
	ticket := insertNode(t, d, tenantA, "TKT-1", "work", "Not a wish", "secret", "open", "", "{}")
	setPortal(t, d, tenantA, true)

	publish := "/api/portal/wishes/" + wish + "/publish"
	if rec := f.do(http.MethodPost, publish, "", "203.0.113.10:1000", nil, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous publish: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPost, publish, `{}`, "203.0.113.10:1000", &member, nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("member publish: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPost, publish, `{}`, "203.0.113.10:1000", &agent, nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("agent publish: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPatch, "/api/portal/products/"+product, `{"title":"Stolen"}`, "203.0.113.10:1000", &member, nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("member product: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPatch, "/api/portal/features/"+feature, `{"status":"live"}`, "203.0.113.10:1000", &agent, nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("agent feature: %d %s", rec.Code, rec.Body)
	}
	if got := portalNodeState(t, d, tenantA, wish); got != "pending" {
		t.Fatalf("wish state after denial: %s", got)
	}
	if got := portalNodeTitle(t, d, tenantA, product); got != "Harbour catalog" {
		t.Fatalf("product title after denial: %s", got)
	}

	if rec := f.do(http.MethodPost, "/api/portal/wishes/"+ticket+"/publish", `{}`, "203.0.113.10:1000", &admin, nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("ticket as wish: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPost, publish, `{"state":"hidden"}`, "203.0.113.10:1000", &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("wish body: %d %s", rec.Code, rec.Body)
	}
	published := f.do(http.MethodPost, publish, "", "203.0.113.10:1000", &admin, nil, nil)
	if published.Code != http.StatusOK || !strings.Contains(published.Body.String(), `"state":"published"`) {
		t.Fatalf("publish: %d %s", published.Code, published.Body)
	}
	if body := publicBody(t, f); !strings.Contains(body, "Quiet wish") || strings.Contains(body, "Noisy wish") {
		t.Fatalf("public after publish: %s", body)
	}
	if rec := f.do(http.MethodPost, "/api/portal/wishes/"+wish+"/reject", "", "203.0.113.10:1000", &admin, nil, nil); rec.Code != http.StatusConflict {
		t.Fatalf("reject published: %d %s", rec.Code, rec.Body)
	}
	hidden := f.do(http.MethodPost, "/api/portal/wishes/"+wish+"/hide", "", "203.0.113.10:1000", &admin, nil, nil)
	if hidden.Code != http.StatusOK || !strings.Contains(hidden.Body.String(), `"state":"hidden"`) {
		t.Fatalf("hide: %d %s", hidden.Code, hidden.Body)
	}
	if body := publicBody(t, f); strings.Contains(body, "Quiet wish") {
		t.Fatalf("hidden wish still public: %s", body)
	}
	if rec := f.do(http.MethodPost, publish, "", "203.0.113.10:1000", &admin, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("republish hidden: %d %s", rec.Code, rec.Body)
	}
	rejected := f.do(http.MethodPost, "/api/portal/wishes/"+other+"/reject", `{}`, "203.0.113.10:1000", &admin, nil, nil)
	if rejected.Code != http.StatusOK || !strings.Contains(rejected.Body.String(), `"state":"rejected"`) {
		t.Fatalf("reject: %d %s", rejected.Code, rejected.Body)
	}

	if rec := f.do(http.MethodPatch, "/api/portal/products/"+product, `{"title":"Morning catalog","note":"no"}`, "203.0.113.10:1000", &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown product field: %d %s", rec.Code, rec.Body)
	}
	renamed := f.do(http.MethodPatch, "/api/portal/products/"+product, `{"title":"Morning catalog"}`, "203.0.113.10:1000", &admin, nil, nil)
	if renamed.Code != http.StatusOK || !strings.Contains(renamed.Body.String(), `"title":"Morning catalog"`) {
		t.Fatalf("rename: %d %s", renamed.Code, renamed.Body)
	}
	if body := publicBody(t, f); !strings.Contains(body, "Morning catalog") || strings.Contains(body, "Harbour catalog") {
		t.Fatalf("public title: %s", body)
	}
	if rec := f.do(http.MethodPatch, "/api/portal/features/"+feature, `{"status":"declined"}`, "203.0.113.10:1000", &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("declined without reason: %d %s", rec.Code, rec.Body)
	}
	declined := f.do(http.MethodPatch, "/api/portal/features/"+feature, `{"status":"declined","decline_reason":"The record is already digital."}`, "203.0.113.10:1000", &admin, nil, nil)
	if declined.Code != http.StatusOK || strings.Contains(declined.Body.String(), "SECRET-NOTE") || !strings.Contains(declined.Body.String(), "already digital") {
		t.Fatalf("decline: %d %s", declined.Code, declined.Body)
	}
	if body := publicBody(t, f); !strings.Contains(body, "already digital") || strings.Contains(body, "SECRET-NOTE") {
		t.Fatalf("public feature: %s", body)
	}
	if fields := portalNodeFields(t, d, tenantA, feature); !strings.Contains(fields, "SECRET-NOTE") {
		t.Fatalf("edit dropped internal fields: %s", fields)
	}
}

func makeAgent(t *testing.T, d *dbtest.DB, tenantID, name, role string, scopes []string) tenant.Principal {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'agent',$2,ARRAY[$3]::text[]) RETURNING id::text`, tenantID, name, role).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tenantID, id, role)
	return tenant.Principal{ID: id, TenantID: tenantID, Kind: tenant.Agent, Name: name, Roles: []string{role}, Scopes: scopes}
}

func publicBody(t *testing.T, f *fixture) string {
	t.Helper()
	rec := f.do(http.MethodGet, "/api/public/portal/mod-a", "", "203.0.113.11:1000", nil, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("public: %d %s", rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func portalNodeState(t *testing.T, d *dbtest.DB, tenantID, id string) string {
	t.Helper()
	var state string
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT state FROM nodes WHERE id=$1::uuid`, id).Scan(&state)
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func portalNodeTitle(t *testing.T, d *dbtest.DB, tenantID, id string) string {
	t.Helper()
	var title string
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT title FROM nodes WHERE id=$1::uuid`, id).Scan(&title)
	})
	if err != nil {
		t.Fatal(err)
	}
	return title
}

func portalNodeFields(t *testing.T, d *dbtest.DB, tenantID, id string) string {
	t.Helper()
	var fields string
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT fields::text FROM nodes WHERE id=$1::uuid`, id).Scan(&fields)
	})
	if err != nil {
		t.Fatal(err)
	}
	return fields
}
