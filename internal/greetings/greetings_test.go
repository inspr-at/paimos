// SPDX-License-Identifier: AGPL-3.0-only

package greetings

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestCorpusLint(t *testing.T) {
	m, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(m.lines); got != 1000 {
		t.Fatalf("got %d lines", got)
	}
	bad := append([]Line(nil), m.lines...)
	bad[1] = bad[0]
	if err := validateCorpus(bad); err == nil {
		t.Fatal("duplicate corpus line passed")
	}
	bad = append([]Line(nil), m.lines...)
	bad[1] = Line{Text: m.lines[0].Text + " today", Tags: []string{"any"}}
	bad[1].ID = lineID(bad[1].Text)
	if err := validateCorpus(bad); err == nil {
		t.Fatal("near duplicate corpus line passed")
	}
}

func TestManifestRegisters(t *testing.T) {
	p, err := ManifestPlugin()
	if err != nil {
		t.Fatal(err)
	}
	reg := plugins.NewRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
}

func TestSelectionUsesContextAndOldestFallback(t *testing.T) {
	lines := []Line{
		{ID: "morning", Tags: []string{"morning"}},
		{ID: "friday", Tags: []string{"friday"}},
		{ID: "night", Tags: []string{"night"}},
	}
	got, err := selectLine(lines, map[string]bool{"night": true}, nil)
	if err != nil || got.ID != "night" {
		t.Fatalf("night draw = %q, %v", got.ID, err)
	}
	history := []shown{{id: "night"}, {id: "morning"}}
	got, err = selectLine(lines, map[string]bool{"night": true}, history)
	if err != nil || got.ID != "night" {
		t.Fatalf("fallback = %q, %v", got.ID, err)
	}
	lines = append(lines, Line{ID: "other-night", Tags: []string{"night"}})
	got, err = selectLine(lines, map[string]bool{"night": true}, history)
	if err != nil || got.ID != "other-night" {
		t.Fatalf("fresh draw = %q, %v", got.ID, err)
	}
	local := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	active := tagsAt(local, true)
	if !active["monday"] || !active["morning"] || !active["return"] || active["friday"] {
		t.Fatalf("Monday context: %+v", active)
	}
}

func TestGreetingRotationPreferencesAndTenantIsolation(t *testing.T) {
	database := dbtest.Open(t)
	ctx := t.Context()
	var tenantA, tenantB, aliceID, bobID string
	for _, row := range []struct {
		slug, name string
		id         *string
	}{
		{"greetings-a", "Greetings A", &tenantA},
		{"greetings-b", "Greetings B", &tenantB},
	} {
		if err := database.Admin.QueryRow(ctx, `INSERT INTO tenants (slug,name) VALUES ($1,$2) RETURNING id::text`, row.slug, row.name).Scan(row.id); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		tenant, name string
		id           *string
	}{
		{tenantA, "Alice Walker", &aliceID},
		{tenantB, "Bob Smith", &bobID},
	} {
		if err := db.InTenant(dbtest.Seed(ctx), database.App, row.tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO principals (tenant_id,kind,name) VALUES ($1::uuid,'person',$2) RETURNING id::text`, row.tenant, row.name).Scan(row.id)
		}); err != nil {
			t.Fatal(err)
		}
	}
	m, err := New(database.App)
	if err != nil {
		t.Fatal(err)
	}
	var baseline int
	if err := database.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1`, tenantA).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	// 10:00 UTC is 03:00 in Los Angeles and 12:00 in Vienna.
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	mux := http.NewServeMux()
	m.Mount(mux)
	draw := func(tenantID, principalID, header string) Greeting {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/me/greeting", nil)
		req.Header.Set("X-Timezone", header)
		req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{TenantID: tenantID, ID: principalID, Kind: tenant.Person}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("greeting: %d %s", w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("greeting is cacheable")
		}
		var greeting Greeting
		if err := json.Unmarshal(w.Body.Bytes(), &greeting); err != nil {
			t.Fatal(err)
		}
		return greeting
	}
	first := draw(tenantA, aliceID, "America/Los_Angeles")
	if first.Salutation != "Hello" || first.Name != "Alice" {
		t.Fatalf("header/local first name: %+v", first)
	}
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO user_preferences (tenant_id,principal_id,key,value) VALUES ($1::uuid,$2::uuid,'timezone','{"timezone":"Europe/Vienna"}'),($1::uuid,$2::uuid,'profile','{"first_name":"Ally"}')`, tenantA, aliceID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{first.ID: true}
	for i := 1; i < 300; i++ {
		g := draw(tenantA, aliceID, "America/Los_Angeles")
		if g.Salutation != "Good afternoon" || g.Name != "Ally" {
			t.Fatalf("preference at draw %d: %+v", i, g)
		}
		if seen[g.ID] {
			t.Fatalf("repeated greeting %s at draw %d", g.ID, i)
		}
		seen[g.ID] = true
	}
	var own, foreign, eventCount int
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantA, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM greeting_history WHERE principal_id=$1::uuid`, aliceID).Scan(&own); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM greeting_history WHERE tenant_id=$1::uuid`, tenantB).Scan(&foreign); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&eventCount)
	}); err != nil {
		t.Fatal(err)
	}
	if own != 300 || foreign != 0 || eventCount != baseline {
		t.Fatalf("history own=%d foreign=%d events=%d", own, foreign, eventCount)
	}
	other := draw(tenantB, bobID, "UTC")
	if other.Name != "Bob" {
		t.Fatalf("other tenant greeting: %+v", other)
	}
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM greeting_history WHERE principal_id=$1::uuid`, aliceID).Scan(&foreign)
	}); err != nil {
		t.Fatal(err)
	}
	if foreign != 0 {
		t.Fatalf("cross-tenant history visible: %d", foreign)
	}
	// The oldest row is pruned after draw 301, and an absence of 72 hours
	// changes the salutation and permits return-tagged lines.
	_ = draw(tenantA, aliceID, "UTC")
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM greeting_history WHERE principal_id=$1::uuid`, aliceID).Scan(&own)
	}); err != nil {
		t.Fatal(err)
	}
	if own != 300 {
		t.Fatalf("history not pruned: %d", own)
	}
	now = now.Add(73 * time.Hour)
	if got := draw(tenantA, aliceID, "UTC"); got.Salutation != "Welcome back" {
		t.Fatalf("return salutation: %+v", got)
	}
}
