// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"
)

func displayRoutesFixture(t *testing.T, p tenant.Principal, count int) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		for i := 1; i <= count; i++ {
			profile, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{Slug: fmt.Sprintf("display-%d", i), Version: "1", Harness: "claude", Family: "anthropic", Model: fmt.Sprintf("fixture-model-%d", i), Effort: "xhigh", Tier: "frontier"})
			if err != nil {
				return err
			}
			if err := insertRoute(t.Context(), tx, p.TenantID, Route{Role: "review-gate", Priority: i, ProfileID: profile.ID, State: "available"}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPoliciesLadderBoundsSetupPermissionsAndTenantIsolation(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "policies-a", "person", "Owner", []string{"admin"})
	other := makePrincipal(t, "policies-b", "person", "Other owner", []string{"admin"})
	before := eventCount(t, p, evSeeded)
	empty := decode[routesRead](t, &p, "GET", "/api/models/routes?role=review-gate", "", 200)
	if empty.Setup || empty.Truncated || len(empty.Steps) != 0 || eventCount(t, p, evSeeded) != before {
		t.Fatal("read seeded the registry or returned steps")
	}
	var profiles int
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM model_profiles`).Scan(&profiles)
	}); err != nil {
		t.Fatal(err)
	}
	if profiles != 0 {
		t.Fatal("unseeded read wrote profiles")
	}
	displayRoutesFixture(t, p, 51)
	got := decode[routesRead](t, &p, "GET", "/api/models/routes?role=review-gate", "", 200)
	if !got.Setup || !got.Truncated || len(got.Steps) != 50 {
		t.Fatalf("setup %v truncated %v steps %d", got.Setup, got.Truncated, len(got.Steps))
	}
	for i, step := range got.Steps {
		if step.Priority != i+1 || step.Profile.Model != fmt.Sprintf("fixture-model-%d", i+1) || step.Profile.ID != step.ProfileID {
			t.Fatalf("wrong order/profile at %d", i)
		}
	}
	foreign := decode[routesRead](t, &other, "GET", "/api/models/routes?role=review-gate", "", 200)
	if foreign.Setup || len(foreign.Steps) != 0 {
		t.Fatal("ladder crossed tenants")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM model_role_routes WHERE priority=51`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	fifty := decode[routesRead](t, &p, "GET", "/api/models/routes?role=review-gate", "", 200)
	if fifty.Truncated || len(fifty.Steps) != 50 {
		t.Fatal("exactly 50 must be complete")
	}
	for _, role := range []string{"scout", "mechanical", "build", "build-hard"} {
		answer := decode[routesRead](t, &p, "GET", "/api/models/routes?role="+role, "", 200)
		if !answer.Setup || len(answer.Steps) != 0 {
			t.Fatal("empty configured role was called unseeded")
		}
	}
	for _, query := range []string{"", "?role=unknown", "?role=build&role=scout"} {
		status, _ := call(t, &p, "GET", "/api/models/routes"+query, "")
		if status != 400 {
			t.Fatalf("role validation %q: %d", query, status)
		}
	}
	status, _ := call(t, nil, "GET", "/api/models/routes?role=build", "")
	if status != 401 {
		t.Fatal("anonymous read")
	}
	reader := addPrincipal(t, p.TenantID, "person", "Settings only", nil)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var rid string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'settings_only','Settings only') RETURNING id::text`, p.TenantID).Scan(&rid); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'settings.read')`, p.TenantID, rid); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, p.TenantID, reader.ID, rid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, body := call(t, &reader, "GET", "/api/models/routes?role=review-gate", "")
	if status != 403 || string(body) != "{\"error\":\"permission denied\"}\n" {
		t.Fatalf("settings-only read: %d %s", status, body)
	}
}

func TestPoliciesDispatchOrderAndContract(t *testing.T) {
	want := []string{"openai", "xai", "anthropic", "cursor", "google", "local"}
	if !reflect.DeepEqual(dispatchFamilyOrder(), want) {
		t.Fatal("dispatch family order changed")
	}
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)
	route := paths["/models/routes"].(map[string]any)
	if route["put"].(map[string]any)["operationId"] != "replaceModelRoutes" {
		t.Fatal("write contract changed")
	}
	get := route["get"].(map[string]any)
	if get["operationId"] != "readModelRoutes" {
		t.Fatal("missing read contract")
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	read := schemas["ModelRoutesRead"].(map[string]any)
	props := read["properties"].(map[string]any)
	if props["steps"].(map[string]any)["maxItems"] != 50 {
		t.Fatal("display bound not contracted")
	}
	for _, key := range []string{"setup", "truncated", "dispatch_family_order", "review_floors"} {
		if props[key] == nil {
			t.Fatalf("missing %s", key)
		}
	}
	if get["responses"].(map[string]any)["503"] == nil {
		t.Fatal("timeout response not contracted")
	}
}

func lockDisplayTable(t *testing.T) (pgx.Tx, int) {
	t.Helper()
	tx, err := adminPool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(t.Context(), `LOCK TABLE model_role_routes IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := tx.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return tx, pid
}

func TestPoliciesLadderStatementTimeout503NoPartialAnswer(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "timeout", "person", "Owner", []string{"admin"})
	displayRoutesFixture(t, p, 1)
	lockDisplayTable(t)
	m := &Module{pool: appPool, routesTimeout: 25 * time.Millisecond}
	r := httptest.NewRequest("GET", "/api/models/routes?role=review-gate", nil)
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	m.readRoutes(w, r)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" || body["error"] != "Review ladder read timed out; retry shortly." || body["steps"] != nil {
		t.Fatalf("wrong timeout response %d %s", w.Code, w.Body.String())
	}
}

func TestPoliciesLadderReplaceReadConsistentSnapshotBarrier(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "snapshot", "person", "Owner", []string{"admin"})
	displayRoutesFixture(t, p, 2)
	old := decode[routesRead](t, &p, "GET", "/api/models/routes?role=review-gate", "", 200)
	now := time.Date(2026, 10, 2, 18, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)
	replacement := []Route{}
	for _, step := range old.Steps {
		replacement = append(replacement, Route{Role: "review-gate", Priority: step.Priority, ProfileID: step.ProfileID, State: "unavailable", Reason: "replacement", ValidUntil: &expires})
	}
	writer, pid := lockDisplayTable(t)
	if _, err := writer.Exec(t.Context(), `SELECT set_config('aeon.tenant_id',$1,true)`, p.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := replaceRoutes(t.Context(), writer, p, replacement, now); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("GET", "/api/models/routes?role=review-gate", nil).WithContext(tenant.WithPrincipal(ctx, p))
		w := httptest.NewRecorder()
		(&Module{pool: appPool}).readRoutes(w, r)
		result <- w
	}()
	// PostgreSQL's lock wait is the barrier: the read must actually overlap the
	// replacement transaction, rather than merely start in another goroutine.
	for {
		var waiting bool
		if err := adminPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks held JOIN pg_locks waiter ON waiter.locktype=held.locktype AND waiter.database=held.database AND waiter.relation=held.relation WHERE held.pid=$1 AND held.granted AND NOT waiter.granted)`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case w := <-result:
			t.Fatalf("read did not overlap write: %d", w.Code)
		default:
		}
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var w *httptest.ResponseRecorder
	select {
	case w = <-result:
	case <-ctx.Done():
		t.Fatal("snapshot read hung")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("snapshot read %d %s", w.Code, w.Body.String())
	}
	var out routesRead
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Steps) != 2 || out.Truncated {
		t.Fatal("partial snapshot")
	}
	for _, step := range out.Steps {
		if step.Reason != out.Steps[0].Reason || step.State != out.Steps[0].State {
			t.Fatal("mixed pre/post replacement snapshot")
		}
	}
	if out.Steps[0].Reason != "" && out.Steps[0].Reason != "replacement" {
		t.Fatal("unrelated snapshot")
	}
}
