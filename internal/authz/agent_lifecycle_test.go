// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// AEON-470: a person retires an agent identity from Access → Agents. The
// directory reports each agent's status, deactivating revokes its keys, service
// identities and connected computers refuse, and an agent key never reaches it.
func TestAgentIdentityDeactivateAndReactivate(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('agent-lifecycle','Agent lifecycle') RETURNING id::text`).Scan(&tid)
	}); err != nil {
		t.Fatal(err)
	}
	admin := tenant.Principal{TenantID: tid, Kind: tenant.Person, Roles: []string{"admin"}, Name: "Bea Admin"}
	owner := tenant.Principal{TenantID: tid, Kind: tenant.Person, Roles: []string{"super_admin"}, Name: "Ada Owner"}
	for _, p := range []*tenant.Principal{&owner, &admin} {
		if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,email,roles) VALUES($1::uuid,'person',$2,$3,$4) RETURNING id::text`, tid, p.Name, strings.ToLower(strings.Fields(p.Name)[0])+"@example.com", p.Roles).Scan(&p.ID); err != nil {
			t.Fatal(err)
		}
		dbtest.BindLegacy(t, d, tid, p.ID)
	}
	agent := func(name string, roles ...string) string {
		t.Helper()
		var id string
		if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'agent',$2,$3) RETURNING id::text`, tid, name, append([]string{}, roles...)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	key := func(principal, prefix string, revoked bool) string {
		t.Helper()
		var id string
		if err := d.Admin.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,revoked_at) VALUES($1::uuid,$2::uuid,'key',$3,$4,'{nodes.read}',CASE WHEN $5 THEN now() END) RETURNING id::text`, tid, principal, prefix, prefix+"-hash", revoked).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// A paired computer: its runtime identity is an agent bound to a computers row.
	codes := 0
	computer := func(name, state string) string {
		t.Helper()
		codes++
		principal := agent(name)
		k := key(principal, name+"-runtime", state == "revoked")
		var request string
		if err := d.Admin.QueryRow(ctx, `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state)
			VALUES($1::uuid,gen_random_uuid(),$2,$3,repeat('b',64),repeat('c',64),'{}'::jsonb,'digest','approved') RETURNING id::text`, tid, fmt.Sprintf("%09d", codes), strings.Repeat(string(rune('a'+codes)), 64)).Scan(&request); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Admin.Exec(ctx, `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,state) VALUES($1::uuid,$2::uuid,$2::uuid,$3::uuid,$4::uuid,$5,repeat('c',64),$6)`, tid, request, principal, k, "paired-"+request, state); err != nil {
			t.Fatal(err)
		}
		return principal
	}
	worker := agent("worker-a")
	activeKey := key(worker, "worker-a-live", false)
	key(worker, "worker-a-old", true)
	idle := agent("worker-b")
	service := agent("Quote service", "quote_confirmation_service")
	connected := computer("laptop", "connected")
	retired := computer("laptop-old", "revoked")

	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	call := func(method, path string, who tenant.Principal) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.Host = "aeon.test"
		req = req.WithContext(tenant.WithPrincipal(req.Context(), who))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	agents := func() map[string]AgentMember {
		t.Helper()
		rec := call("GET", "/api/members", admin)
		if rec.Code != 200 {
			t.Fatalf("members %d %s", rec.Code, rec.Body.String())
		}
		var dir MemberDirectory
		if err := json.Unmarshal(rec.Body.Bytes(), &dir); err != nil {
			t.Fatal(err)
		}
		out := map[string]AgentMember{}
		for _, a := range dir.Agents {
			out[a.PrincipalID] = a
		}
		return out
	}

	before := agents()
	for id, a := range before {
		if a.Status != "active" {
			t.Fatalf("%s starts %q", a.Name, a.Status)
		}
		if a.ConnectedComputer != (id == connected) {
			t.Fatalf("%s connected_computer=%v", a.Name, a.ConnectedComputer)
		}
		// Retired computers stay marked: the directory must never offer them a key.
		if a.PairedComputer != (id == connected || id == retired) {
			t.Fatalf("%s paired_computer=%v", a.Name, a.PairedComputer)
		}
	}
	if before[worker].KeyCount != 1 {
		t.Fatalf("worker-a has %d non-revoked keys, want 1", before[worker].KeyCount)
	}

	// Deactivating revokes the keys and is one audited event.
	if rec := call("POST", "/api/members/"+worker+"/deactivate", admin); rec.Code != 200 {
		t.Fatalf("deactivate agent %d %s", rec.Code, rec.Body.String())
	}
	after := agents()[worker]
	if after.Status != "deactivated" || after.KeyCount != 0 {
		t.Fatalf("after deactivate: %q with %d keys", after.Status, after.KeyCount)
	}
	var revokedAt *string
	if err := d.Admin.QueryRow(ctx, `SELECT revoked_at::text FROM agent_keys WHERE id=$1::uuid`, activeKey).Scan(&revokedAt); err != nil || revokedAt == nil {
		t.Fatalf("the live key stays usable: %v %v", revokedAt, err)
	}
	var events int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='principal.deactivated' AND after->>'principal_id'=$1 AND actor_principal_id=$2::uuid`, worker, admin.ID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("deactivation events %d %v", events, err)
	}
	if rec := call("POST", "/api/members/"+worker+"/deactivate", admin); rec.Code != 409 || !strings.Contains(rec.Body.String(), "This agent is already deactivated") {
		t.Fatalf("second deactivate %d %s", rec.Code, rec.Body.String())
	}

	// Reactivating brings the identity back; the revoked key stays revoked.
	if rec := call("POST", "/api/members/"+worker+"/reactivate", admin); rec.Code != 200 {
		t.Fatalf("reactivate agent %d %s", rec.Code, rec.Body.String())
	}
	if back := agents()[worker]; back.Status != "active" || back.KeyCount != 0 {
		t.Fatalf("after reactivate: %q with %d keys", back.Status, back.KeyCount)
	}
	if rec := call("POST", "/api/members/"+worker+"/reactivate", admin); rec.Code != 409 || !strings.Contains(rec.Body.String(), "This agent is already active") {
		t.Fatalf("second reactivate %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("POST", "/api/members/"+idle+"/deactivate", owner); rec.Code != 200 {
		t.Fatalf("deactivate keyless agent %d %s", rec.Code, rec.Body.String())
	}

	// Internal service identities and connected computers refuse; a revoked computer's identity does not.
	if rec := call("POST", "/api/members/"+service+"/deactivate", admin); rec.Code != 403 {
		t.Fatalf("service identity %d %s", rec.Code, rec.Body.String())
	}
	// Every reserved service identity is classified once: the directory flags it
	// and the lifecycle refuses it, including the portal's public service.
	// The classification fails closed: a role tag nobody listed (a future service,
	// a typo, a tag outside the quote_ family) is a service identity too.
	for _, role := range []string{"system", "importer", "operator", "embedding", "quote_public_service", "quote_confirmation_service", "portal_public_service", "portal_future_service", "worker"} {
		id := agent("svc "+role, role)
		if !agents()[id].Service {
			t.Fatalf("%s is not flagged as a service identity in the directory", role)
		}
		if rec := call("POST", "/api/members/"+id+"/deactivate", admin); rec.Code != 403 {
			t.Fatalf("%s deactivate %d %s", role, rec.Code, rec.Body.String())
		}
		if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
			retired, err := RetireAgentTx(ctx, tx, admin, id, nil)
			if retired {
				t.Fatalf("%s was retired by the pairing path", role)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if agents()[id].Status != "active" {
			t.Fatalf("a refused deactivation changed the %s identity", role)
		}
	}
	if rec := call("POST", "/api/members/"+connected+"/deactivate", admin); rec.Code != 409 || !strings.Contains(rec.Body.String(), "connected_computer") {
		t.Fatalf("connected computer %d %s", rec.Code, rec.Body.String())
	}
	if agents()[connected].Status != "active" {
		t.Fatal("a refused deactivation changed the connected computer's identity")
	}
	if rec := call("POST", "/api/members/"+retired+"/deactivate", admin); rec.Code != 200 {
		t.Fatalf("revoked computer %d %s", rec.Code, rec.Body.String())
	}

	// It is person-only: an agent key, even with a scope that names it, is refused.
	bot := tenant.Principal{ID: idle, TenantID: tid, Kind: tenant.Agent, Name: "worker-b", Scopes: []string{"members.read"}}
	if rec := call("POST", "/api/members/"+worker+"/deactivate", bot); rec.Code != 403 {
		t.Fatalf("agent caller %d %s", rec.Code, rec.Body.String())
	}
	if agents()[worker].Status != "active" {
		t.Fatal("an agent deactivated another agent")
	}
	if perm, ok := Lookup("members.manage"); !ok || perm.AgentGrantable {
		t.Fatal("members.manage must never be agent grantable")
	}
}
