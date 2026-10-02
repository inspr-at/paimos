// SPDX-License-Identifier: AGPL-3.0-only

package views

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestAgentsPlanPrivateOwnershipValidationAndCounts(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var tid, foreign string
	must(d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('agents-plan','Plan') RETURNING id::text`).Scan(&tid))
	must(d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('agents-plan-foreign','Foreign') RETURNING id::text`).Scan(&foreign))
	person := func(tenantID, kind, name string) tenant.Principal {
		t.Helper()
		p := tenant.Principal{TenantID: tenantID, Kind: tenant.PrincipalKind(kind)}
		must(d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,$3) RETURNING id::text`, tenantID, kind, name).Scan(&p.ID))
		dbtest.BindRole(t, d, tenantID, p.ID, "viewer")
		return p
	}
	alice := person(tid, "person", "Alice")
	var planRole string
	must(d.Admin.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'plan_only','Own plan only') RETURNING id::text`, tid).Scan(&planRole))
	_, err := d.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, tid, planRole, agentplan.ReadScope)
	must(err)
	_, err = d.Admin.Exec(ctx, `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1`, alice.ID, planRole)
	must(err)
	bob := person(tid, "person", "Bob")
	carol := person(foreign, "person", "Carol")
	agent := person(tid, "agent", "Alice's enforcer")
	agent.KeyCreatorID, agent.Scopes = alice.ID, []string{agentplan.ReadScope}
	foreignAgent := person(foreign, "agent", "Foreign enforcer")
	foreignAgent.KeyCreatorID, foreignAgent.Scopes = carol.ID, []string{agentplan.ReadScope}
	var project, foreignProject string
	must(d.Admin.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'PLAN-1','Plan project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&project))
	must(d.Admin.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'PLAN-1','Plan project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, foreign).Scan(&foreignProject))
	addSession := func(p tenant.Principal, owner *string, project, harness, phase, activity string, archived bool) string {
		t.Helper()
		var id string
		must(d.Admin.QueryRow(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,ref_digest,lease_digest,phase,activity,stopped_at)
			VALUES($1,$2,$3,$4,$5,'test','unmanaged','worker',uuid_send(gen_random_uuid()),uuid_send(gen_random_uuid()),$6,$7,
			CASE WHEN $6='stopped' THEN now() END) RETURNING id::text`, p.TenantID, project, p.ID, owner, harness, phase, activity).Scan(&id))
		if archived {
			_, err := d.Admin.Exec(ctx, `UPDATE harness_sessions SET archived_at=now(),recovery_process_state='unknown',recovery_request_id=gen_random_uuid(),recovery_request_digest=uuid_send(gen_random_uuid()),recovery_actor_id=$2,recovery_reason='Test recovery' WHERE id=$1`, id, *owner)
			must(err)
		}
		return id
	}
	// A person with only plan permission cannot see project sessions, but the private
	// aggregate must still count their sessions across every project.
	addSession(agent, &alice.ID, project, "codex", "starting", "unknown", false)
	addSession(agent, &alice.ID, project, "codex", "working", "idle", false)
	addSession(agent, &alice.ID, project, "cursor", "yielded", "throttled", false)
	addSession(agent, &alice.ID, project, "claude", "stopping", "busy", false)
	addSession(agent, &alice.ID, project, "codex", "stopped", "idle", false)
	addSession(agent, &alice.ID, project, "codex", "stopped", "busy", true)
	addSession(agent, &bob.ID, project, "grok", "working", "busy", false)
	addSession(foreignAgent, &carol.ID, foreignProject, "pi", "working", "busy", false)
	// Ownerless legacy sessions count once even with several keys from one
	// person. Keys identify historical ownership even after revocation.
	legacy := person(tid, "agent", "Legacy")
	addSession(legacy, nil, project, "codex", "working", "busy", false)
	for range 2 {
		_, err := d.Admin.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id,revoked_at) VALUES($1,$2,'test',gen_random_uuid()::text,'test-only-not-a-credential',$3,now())`, tid, legacy.ID, alice.ID)
		must(err)
	}
	ambiguous := person(tid, "agent", "Ambiguous legacy")
	addSession(ambiguous, nil, project, "grok", "working", "busy", false)
	for _, creator := range []string{alice.ID, bob.ID} {
		_, err := d.Admin.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id) VALUES($1,$2,'test',gen_random_uuid()::text,'test-only-not-a-credential',$3)`, tid, ambiguous.ID, creator)
		must(err)
	}
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	request := func(p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if p.ID != "" {
			r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	snapshot := func(p tenant.Principal, path string) agentsPlanSnapshot {
		t.Helper()
		w := request(p, "GET", path, "")
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("plan status=%d body=%s", w.Code, w.Body.String())
		}
		var out agentsPlanSnapshot
		must(json.Unmarshal(w.Body.Bytes(), &out))
		return out
	}
	if out := snapshot(alice, "/api/agents/plan"); out.Total != 15 || out.Source != "default" || out.UpdatedAt != nil || out.RunningTotal != 5 || out.Running["codex"] != 3 || out.Running["cursor"] != 1 || out.Running["claude"] != 1 || out.Running["grok"] != 0 {
		t.Fatalf("default/counts %+v", out)
	}
	for _, value := range []string{`{"cap":8,"view":"model","model":{"codex":4}}`, `{"total":0,"limits":{"cursor":"off","codex":0}}`, `{"total":30,"limits":{"claude":30,"codex":30}}`} {
		w := request(alice, "PUT", "/api/preferences/agents.working", `{"value":`+value+`}`)
		if w.Code != 200 {
			t.Fatalf("save status=%d body=%s", w.Code, w.Body.String())
		}
		want, source, err := agentplan.Decode([]byte(value))
		must(err)
		for _, reader := range []tenant.Principal{alice, agent} {
			// Hostile selectors never change the identity-derived owner.
			out := snapshot(reader, "/api/agents/plan?principal_id="+bob.ID+"&tenant_id="+foreign)
			if out.PrincipalID != alice.ID || out.Total != want.Total || out.Source != source || out.RunningTotal != 5 || out.UpdatedAt == nil {
				t.Fatalf("owner snapshot %+v", out)
			}
		}
		if got := request(alice, "GET", "/api/preferences/agents.working", ""); strings.Contains(value, `"cap"`) && !strings.Contains(got.Body.String(), `"cap"`) {
			t.Fatal("legacy preference was rewritten")
		}
	}
	if out := snapshot(bob, "/api/agents/plan"); out.Total != 15 || out.PrincipalID != bob.ID || out.RunningTotal != 1 {
		t.Fatalf("other person %+v", out)
	}
	if out := snapshot(foreignAgent, "/api/agents/plan"); out.Total != 15 || out.PrincipalID != carol.ID || out.RunningTotal != 1 {
		t.Fatalf("other tenant %+v", out)
	}
	for _, value := range []string{`{"total":31}`, `{"total":-1}`, `{"total":null}`, `{"total":5,"limits":{"codex":31}}`, `{"total":5,"limits":{"codex":null}}`} {
		if w := request(alice, "PUT", "/api/preferences/agents.working", `{"value":`+value+`}`); w.Code != 400 {
			t.Fatalf("invalid save status=%d", w.Code)
		}
	}
	if out := snapshot(alice, "/api/agents/plan"); out.Total != 30 {
		t.Fatal("rejected write changed plan")
	}
	if w := request(agent, "PUT", "/api/preferences/agents.working", `{"value":{"total":0}}`); w.Code != 403 {
		t.Fatal("agent wrote plan")
	}
	for _, p := range []tenant.Principal{
		{ID: agent.ID, TenantID: tid, Kind: tenant.Agent, KeyCreatorID: alice.ID},
		{ID: agent.ID, TenantID: tid, Kind: tenant.Agent, Scopes: []string{agentplan.ReadScope}},
		{ID: agent.ID, TenantID: tid, Kind: tenant.Agent, KeyCreatorID: carol.ID, Scopes: []string{agentplan.ReadScope}},
	} {
		if w := request(p, "GET", "/api/agents/plan", ""); w.Code != 403 {
			t.Fatalf("unauthorized delegation status=%d", w.Code)
		}
	}
	if w := request(tenant.Principal{}, "GET", "/api/agents/plan", ""); w.Code != 401 {
		t.Fatal("anonymous plan read")
	}
	// Stored corruption never creates an accidental default start allowance.
	_, err = d.Admin.Exec(ctx, `UPDATE user_preferences SET value='{"total":99}'::jsonb WHERE tenant_id=$1 AND principal_id=$2 AND key='agents.working'`, tid, alice.ID)
	must(err)
	if w := request(agent, "GET", "/api/agents/plan", ""); w.Code != 500 {
		t.Fatal("invalid stored plan did not fail closed")
	}
}
