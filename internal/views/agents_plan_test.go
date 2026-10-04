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
	// Preference writes require views.write at the route and in the final
	// transaction; this grant still exposes no project/session information.
	_, err = d.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'views.write')`, tid, planRole)
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

func TestAgentsPlanLinkedPersonReadWriteAndConflicts(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var tid, project string
	must(d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('linked-plan','Linked plan') RETURNING id::text`).Scan(&tid))
	must(d.Admin.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'LINKED-1','Project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&project))
	person := func(kind, name string, linked *string) tenant.Principal {
		t.Helper()
		p := tenant.Principal{TenantID: tid, Kind: tenant.PrincipalKind(kind)}
		must(d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,linked_to) VALUES($1,$2,$3,$4) RETURNING id::text`, tid, kind, name, linked).Scan(&p.ID))
		if linked == nil {
			dbtest.BindRole(t, d, tid, p.ID, "viewer")
			if kind == "person" {
				// Canonical people make preference writes in this fixture.
				dbtest.BindRole(t, d, tid, p.ID, "member")
			}
		}
		return p
	}
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	request := func(p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		name, canonical, alias, secondAlias, source string
		total, status                               int
	}{
		{name: "unset family", total: 15, source: "default", status: 200},
		{name: "canonical zero blocks alias key", canonical: `{"total":0}`, total: 0, source: "plan", status: 200},
		{name: "alias only zero", alias: `{"total":0}`, total: 0, source: "plan", status: 200},
		{name: "alias only legacy", alias: `{"cap":6,"view":"model","model":{"codex":4}}`, total: 6, source: "legacy", status: 200},
		{name: "equivalent legacy and explicit no limit", canonical: `{"cap":6,"area":{"dev":2}}`, alias: `{"total":6,"limits":{"codex":"no_limit"}}`, total: 6, source: "legacy", status: 200},
		{name: "conflicting totals", canonical: `{"total":5}`, alias: `{"total":0}`, status: 500},
		{name: "conflicting harness limits", canonical: `{"total":5,"limits":{"codex":"off"}}`, alias: `{"total":5}`, status: 500},
		{name: "off and numeric zero stay distinct", canonical: `{"total":5,"limits":{"codex":"off"}}`, alias: `{"total":5,"limits":{"codex":0}}`, status: 500},
		{name: "conflicting aliases without canonical row", alias: `{"total":0}`, secondAlias: `{"total":5}`, status: 500},
		{name: "malformed canonical is not masked", canonical: `{"total":99}`, alias: `{"total":0}`, status: 500},
		{name: "malformed alias is not masked", canonical: `{"total":0}`, alias: `{"total":99}`, status: 500},
		{name: "malformed alias only", alias: `{"total":99}`, status: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			canonical := person("person", tc.name, nil)
			alias := person("person", "Linked alias", &canonical.ID)
			secondAlias := person("person", "Second alias", &canonical.ID)
			agent := person("agent", "Enforcer", nil)
			agent.KeyCreatorID, agent.Scopes = canonical.ID, []string{agentplan.ReadScope}
			aliasKey := agent
			aliasKey.KeyCreatorID = alias.ID
			// The canonical save is a real preference request, including the
			// reported regression: total zero read by a key created by an alias.
			if tc.canonical != "" && tc.canonical != `{"total":99}` {
				if w := request(canonical, "PUT", "/api/preferences/agents.working", `{"value":`+tc.canonical+`}`); w.Code != 200 {
					t.Fatalf("canonical save status=%d body=%s", w.Code, w.Body.String())
				}
			}
			for _, saved := range []struct{ owner, value string }{
				{canonical.ID, tc.canonical}, {alias.ID, tc.alias}, {secondAlias.ID, tc.secondAlias},
			} {
				if saved.value != "" {
					_, err := d.Admin.Exec(ctx, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agents.working',$3::jsonb)
						ON CONFLICT(tenant_id,principal_id,key) DO UPDATE SET value=EXCLUDED.value`, tid, saved.owner, saved.value)
					must(err)
				}
			}
			for _, owner := range []*string{&canonical.ID, &alias.ID, nil} {
				_, err := d.Admin.Exec(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,ref_digest,lease_digest,phase,activity)
					VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker',uuid_send(gen_random_uuid()),uuid_send(gen_random_uuid()),'working','busy')`, tid, project, agent.ID, owner)
				must(err)
			}
			_, err := d.Admin.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id)
				VALUES($1,$2,'alias key',gen_random_uuid()::text,'test-only-not-a-credential',$3)`, tid, agent.ID, alias.ID)
			must(err)
			for _, reader := range []tenant.Principal{canonical, alias, agent, aliasKey} {
				w := request(reader, "GET", "/api/agents/plan", "")
				if w.Code != tc.status {
					t.Fatalf("reader %s plan status=%d want=%d body=%s", reader.ID, w.Code, tc.status, w.Body.String())
				}
				if tc.status == 200 {
					var out agentsPlanSnapshot
					must(json.Unmarshal(w.Body.Bytes(), &out))
					if out.PrincipalID != canonical.ID || out.Total != tc.total || out.Source != tc.source || out.RunningTotal != 3 || out.Running["codex"] != 3 || (out.UpdatedAt == nil) != (tc.source == "default") {
						t.Fatalf("linked plan %+v", out)
					}
					if tc.total == 0 {
						if ok, reason := agentplan.CanStart(out.Plan, out.Running, "codex"); ok || reason != "planned total reached" {
							t.Fatalf("zero plan allows start: %v %s", ok, reason)
						}
					}
				} else if strings.Contains(w.Body.String(), "total") || strings.Contains(w.Body.String(), "principal_id") {
					t.Fatal("conflict exposed a usable plan snapshot")
				}
			}
			for _, reader := range []tenant.Principal{canonical, alias} {
				w := request(reader, "GET", "/api/preferences/agents.working", "")
				if w.Code != tc.status {
					t.Fatalf("preference status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
				}
				if tc.status == 200 {
					var out preference
					must(json.Unmarshal(w.Body.Bytes(), &out))
					want := tc.canonical
					if want == "" {
						want = tc.alias
					}
					if want == "" {
						want = "null"
					}
					var gotJSON, wantJSON any
					must(json.Unmarshal(out.Value, &gotJSON))
					must(json.Unmarshal([]byte(want), &wantJSON))
					got, _ := json.Marshal(gotJSON)
					expected, _ := json.Marshal(wantJSON)
					if string(got) != string(expected) {
						t.Fatalf("raw preference %s want=%s", got, expected)
					}
				}
			}
			// Saving as the alias must write the canonical row, recover from
			// conflicting legacy copies, and never interrupt existing work.
			if w := request(alias, "PUT", "/api/preferences/agents.working", `{"value":{"total":0,"limits":{"cursor":"off"}}}`); w.Code != 200 {
				t.Fatalf("alias save status=%d body=%s", w.Code, w.Body.String())
			}
			var canonicalValue []byte
			must(d.Admin.QueryRow(ctx, `SELECT value FROM user_preferences WHERE tenant_id=$1 AND principal_id=$2 AND key='agents.working'`, tid, canonical.ID).Scan(&canonicalValue))
			var saved agentplan.Plan
			must(json.Unmarshal(canonicalValue, &saved))
			if saved.Total != 0 || saved.Limits["cursor"].Mode != agentplan.Off {
				t.Fatalf("canonical value after alias save: %s", canonicalValue)
			}
			for _, reader := range []tenant.Principal{canonical, alias, agent, aliasKey} {
				w := request(reader, "GET", "/api/agents/plan", "")
				if w.Code != 200 {
					t.Fatalf("reconciled plan status=%d body=%s", w.Code, w.Body.String())
				}
				var out agentsPlanSnapshot
				must(json.Unmarshal(w.Body.Bytes(), &out))
				if out.PrincipalID != canonical.ID || out.Total != 0 || out.RunningTotal != 3 || out.Limits["cursor"].Mode != agentplan.Off {
					t.Fatalf("reconciled snapshot %+v", out)
				}
			}
			// Ordinary UI preferences still belong to each raw principal.
			if w := request(alias, "PUT", "/api/preferences/list:p1", `{"value":{"split":0.4}}`); w.Code != 200 {
				t.Fatalf("ordinary preference save status=%d", w.Code)
			}
			w := request(canonical, "GET", "/api/preferences/list:p1", "")
			var out preference
			must(json.Unmarshal(w.Body.Bytes(), &out))
			if w.Code != 200 || string(out.Value) != "null" {
				t.Fatal("ordinary preferences leaked across aliases")
			}
		})
	}
}
