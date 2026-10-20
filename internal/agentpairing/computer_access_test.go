// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: pairing provenance grants start authority, computer grants leak into
// projects/other computers, or access changes are unaudited or cross-tenant.
func TestComputerStartAccessUsesPersonRoleBindingsAndAuditsChanges(t *testing.T) {
	f := newFixture(t)
	first := f.propose("claude")
	f.approve(first, "connect_only")
	a := f.redeem(first)
	second := f.propose("codex")
	f.approve(second, "connect_only")
	b := f.redeem(second)
	admin := tenant.Principal{ID: f.person, TenantID: f.tenantID, Kind: tenant.Person}
	person := tenant.Principal{TenantID: f.tenantID, Kind: tenant.Person}
	var role, other string
	seed := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, fn); err != nil {
			t.Fatal(err)
		}
	}
	seed(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Computer user') RETURNING id::text`, f.tenantID).Scan(&person.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Another user') RETURNING id::text`, f.tenantID).Scan(&other); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'computer_starter','Computer starter') RETURNING id::text`, f.tenantID).Scan(&role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3),($1,$2,'nodes.read')`, f.tenantID, role, authz.StartOnComputer)
		return err
	})
	can := func(p tenant.Principal, computer string, want bool) {
		t.Helper()
		seed(func(tx pgx.Tx) error {
			got, err := authz.CanStartOnComputerTx(t.Context(), tx, p, computer)
			if err == nil && got != want {
				t.Fatalf("%s computer %s = %v, want %v", p.ID, computer, got, want)
			}
			return err
		})
	}
	for _, computer := range []string{*a.ComputerID, *b.ComputerID} {
		can(admin, computer, true)
		can(person, computer, false)
	}
	can(tenant.Principal{ID: *a.PrincipalID, TenantID: f.tenantID, Kind: tenant.Agent}, *a.ComputerID, false)
	seed(func(tx pgx.Tx) error {
		var granted bool
		if err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM role_bindings rb JOIN roles r ON r.tenant_id=rb.tenant_id AND r.id=rb.role_id JOIN role_permissions rp ON rp.tenant_id=r.tenant_id AND rp.role_id=r.id WHERE rb.principal_id=$1 AND r.key LIKE 'paired_%' AND rp.permission=$2)`, *a.PrincipalID, authz.StartOnComputer).Scan(&granted); err != nil {
			return err
		}
		if granted || slices.Contains(agentpairing.RuntimePermissions, authz.StartOnComputer) {
			t.Fatal("pairing issued computer start authority")
		}
		return nil
	})
	path := "/api/agent-pairing/computers/" + *a.ComputerID + "/access"
	var summary agentpairing.ComputerStartAccess
	decodeResult(t, f.call("GET", path, nil, true, "", 200), &summary)
	if !summary.Admins || len(summary.People) != 0 {
		t.Fatalf("pairing must leave Admins only: %+v", summary)
	}
	for _, id := range []string{person.ID, other} {
		var binding agentpairing.ComputerAccessBinding
		decodeResult(t, f.call("PUT", path+"/"+id, map[string]string{"role_id": role}, true, "", 200), &binding)
		if binding.ComputerID != *a.ComputerID || binding.PrincipalID != id || binding.ScopeType != "computer" || binding.Role.ID != role {
			t.Fatalf("wrong computer binding: %+v", binding)
		}
	}
	can(person, *a.ComputerID, true)
	can(person, *b.ComputerID, false)
	// The existing role editor must continue refusing deletion/reassignment of
	// a role used at an expanded scope; it must not silently rewrite that grant.
	roles := http.NewServeMux()
	authz.New(f.db.App).Mount(roles)
	var replacement string
	seed(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id::text FROM roles WHERE key='admin'`).Scan(&replacement)
	})
	for _, suffix := range []string{"", "?reassign_to=" + replacement} {
		r := httptest.NewRequest("DELETE", "/api/roles/"+role+suffix, nil).WithContext(tenant.WithPrincipal(t.Context(), admin))
		w := httptest.NewRecorder()
		roles.ServeHTTP(w, r)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "role_in_use") {
			t.Fatalf("role deletion bypassed computer binding: %d %s", w.Code, w.Body.String())
		}
	}
	// A role containing nodes.read grants no project when bound to a computer.
	seed(func(tx pgx.Tx) error {
		if err := authz.RequireTx(t.Context(), tx, person, "nodes.read", authz.Scope{ProjectID: *b.ComputerID}); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("computer grant opened a project: %v", err)
		}
		return nil
	})
	seen := map[string]bool{}
	pagePath := path + "?limit=1"
	for {
		decodeResult(t, f.call("GET", pagePath, nil, true, "", 200), &summary)
		if len(summary.People) != 1 || seen[summary.People[0].PrincipalID] {
			t.Fatalf("summary pagination: %+v", summary)
		}
		seen[summary.People[0].PrincipalID] = true
		if summary.NextCursor == nil {
			break
		}
		pagePath = path + "?limit=1&cursor=" + *summary.NextCursor
	}
	if len(seen) != 2 || !seen[person.ID] || !seen[other] {
		t.Fatal("summary omitted granted people")
	}
	// Exact replay emits no second grant event, and revoke affects just A.
	f.call("PUT", path+"/"+person.ID, map[string]string{"role_id": role}, true, "", 200)
	f.call("DELETE", path+"/"+person.ID, nil, true, "", 204)
	can(person, *a.ComputerID, false)
	f.call("DELETE", path+"/"+person.ID, nil, true, "", 404)
	seed(func(tx pgx.Tx) error {
		var grants, revokes int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE type='agent_pairing.computer_access_granted'),count(*) FILTER(WHERE type='agent_pairing.computer_access_revoked') FROM events WHERE tenant_id=$1 AND actor_principal_id=$2`, f.tenantID, f.person).Scan(&grants, &revokes); err != nil {
			return err
		}
		if grants != 2 || revokes != 1 {
			t.Fatalf("grant/revoke audit counts %d/%d, want 2/1", grants, revokes)
		}
		return nil
	})
	for _, query := range []string{"?limit=0", "?limit=101", "?cursor=invalid"} {
		f.call("GET", path+query, nil, true, "", 400)
	}
	f.call("PUT", path+"/"+*a.PrincipalID, map[string]string{"role_id": role}, true, "", 400)
	f.call("PUT", path+"/"+person.ID, map[string]string{"role_id": role, "group_id": other}, true, "", 400)
	f.call("PUT", path+"/"+person.ID, map[string]string{"role_id": role}, false, "aeon_"+a.RuntimePrefix+"_"+first.runtime, 403)
	foreign := newFixtureInTenant(t, f.db, "computer-access-other")
	foreign.call("GET", path, nil, true, "", 404)
	foreign.call("PUT", path+"/"+person.ID, map[string]string{"role_id": role}, true, "", 404)
	// Keep a different workspace Owner so the fixture can demote its admin.
	dbtest.BindRole(t, f.db, f.tenantID, other, "owner")
	dbtest.BindRole(t, f.db, f.tenantID, f.person, "member")
	can(admin, *a.ComputerID, false)
	f.call("PUT", path+"/"+person.ID, map[string]string{"role_id": role}, true, "", 403)
}
