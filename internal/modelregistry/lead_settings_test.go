// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestLeadPolicyInheritanceAndBounds(t *testing.T) {
	ids := []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
	empty := []string{}
	wide := []string{ids[0], "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}
	parent := LeadPolicy{AllowedAccountIDs: &ids, Recovery: &LeadRecovery{2, 10}}
	child := LeadPolicy{AllowedAccountIDs: &wide, Recovery: &LeadRecovery{5, 20}}
	effective := effectiveLeadPolicy(parent, child)
	if len(*effective.AllowedAccountIDs) != 1 || effective.Recovery.MaxAttempts != 2 || effective.Recovery.AgentHours != 10 {
		t.Fatal(effective)
	}
	effective = effectiveLeadPolicy(parent, LeadPolicy{AllowedAccountIDs: &empty})
	if effective.AllowedAccountIDs == nil || len(*effective.AllowedAccountIDs) != 0 {
		t.Fatal("empty restriction must deny every account")
	}
	effective = effectiveLeadPolicy(LeadPolicy{}, child)
	if effective.Recovery.MaxAttempts != 0 || effective.Recovery.AgentHours != 0 {
		t.Fatal("project must not enable workspace recovery")
	}
	for _, raw := range []string{`{"bucket":"unknown"}`, `{"allowed_host_ids":["bad"]}`, `{"allowed_account_ids":["AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA","aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"]}`, `{"recovery":{"max_attempts":6,"agent_hours":10}}`, `{"recovery":{"max_attempts":0,"agent_hours":10}}`, `{"recovery":{"max_attempts":1,"agent_hours":0}}`} {
		var p LeadPolicy
		if json.Unmarshal([]byte(raw), &p) != nil || validateLeadPolicy(p) == nil {
			t.Fatalf("accepted invalid policy: %s", raw)
		}
	}
}

func leadProject(t *testing.T, p tenant.Principal) string {
	t.Helper()
	var project string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state) SELECT $1,id,'LEAD-1','Lead policy','active' FROM node_kinds WHERE slug='project' RETURNING id::text`, p.TenantID).Scan(&project)
	}); err != nil {
		t.Fatal(err)
	}
	return project
}

func TestLeadSettingsRevisionsOwnershipInheritanceAndRedaction(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "lead-policy", "person", "Owner", []string{"admin"})
	otherAdmin := addPrincipal(t, owner.TenantID, "person", "Other admin", []string{"admin"})
	member := addPrincipal(t, owner.TenantID, "person", "Member", []string{"member"})
	agent := addPrincipal(t, owner.TenantID, "agent", "Agent", []string{"admin"})
	agent.Scopes = []string{"nodes.read", "models.read", "model_prefs.manage"}
	dbtest.BindRole(t, testDB, owner.TenantID, agent.ID, "member")
	agent.KeyCreatorID = owner.ID
	foreign := makePrincipal(t, "foreign-lead", "person", "Foreign", []string{"admin"})
	project := leadProject(t, owner)
	path := "/api/projects/" + project + "/lead-settings"
	doc := prefDoc(t, owner)
	kind := kindID(t, doc, "backend")
	empty := decode[LeadSettings](t, &owner, "GET", path, "", 200)
	if empty.Revision != 0 || empty.OwnerPersonID != nil || empty.AutomaticLaunchEnabled || empty.Effective.Recovery.MaxAttempts != 0 {
		t.Fatal(empty)
	}
	decode[LeadSettings](t, &owner, "PUT", "/api/settings/lead-policy", `{"revision":0,"overrides":{"recovery":{"max_attempts":3,"agent_hours":12}}}`, 200)
	body := `{"revision":0,"overrides":{"work_kind_id":"` + kind + `","bucket":"complex","allowed_host_ids":[],"allowed_account_ids":[],"recovery":{"max_attempts":5,"agent_hours":20}}}`
	saved := decode[LeadSettings](t, &owner, "PUT", path, body, 200)
	if saved.Revision != 1 || saved.WorkspaceRevision != 1 || saved.OwnerPersonID == nil || *saved.OwnerPersonID != owner.ID || saved.DetailsRedacted || saved.Effective.Recovery.MaxAttempts != 3 || saved.Effective.Recovery.AgentHours != 12 || saved.ModelSelector == nil || len(saved.ModelRevisions) != 3 {
		t.Fatal(saved)
	}
	expectPrefError(t, owner, "PUT", path, body, 409, "stale_revision")
	expectPrefError(t, otherAdmin, "PUT", path, `{"revision":1,"overrides":{}}`, 403, "owner_required")
	expectPrefError(t, otherAdmin, "DELETE", path+"?revision=1", "", 403, "owner_required")
	expectPrefError(t, member, "PUT", path, `{"revision":1,"overrides":{}}`, 403, "permission")
	expectPrefError(t, agent, "PUT", path, `{"revision":1,"overrides":{}}`, 403, "person_required")
	expectPrefError(t, foreign, "GET", path, "", 404, "not found")
	for _, p := range []tenant.Principal{otherAdmin, member, agent} {
		public := decode[LeadSettings](t, &p, "GET", path, "", 200)
		if !public.DetailsRedacted || public.Overrides != nil || public.ModelSelector != nil || public.Effective.AllowedHostIDs != nil || public.Effective.AllowedAccountIDs != nil || public.Effective.WorkKindID != nil || public.AutomaticLaunchEnabled {
			t.Fatal("private selector leaked", public)
		}
	}
	// Editing the existing model matrix changes this view without copying a route.
	profiles := decode[[]Profile](t, &owner, "GET", "/api/models", "", 200)
	cell := modelprefs.Cell{Mode: "pinned", ProfileID: profileBySlug(profiles, "codex-sol-high").ID}
	decode[preferenceWriteResult](t, &owner, "PUT", "/api/model-preferences/levels/default/rows/"+kind, prefRowBody(0, cell, cell, false), 200)
	current := decode[LeadSettings](t, &owner, "GET", path, "", 200)
	if current.ModelSelector.Cell == nil || current.ModelSelector.Cell.ProfileID != cell.ProfileID || current.ModelSelector.SetBy != "default" || current.ModelRevisions[0] != 1 {
		t.Fatal("model route was copied or lost", current)
	}
	decode[LeadSettings](t, &owner, "PUT", "/api/settings/lead-policy", `{"revision":1,"overrides":{"recovery":{"max_attempts":1,"agent_hours":2}}}`, 200)
	current = decode[LeadSettings](t, &owner, "GET", path, "", 200)
	if current.WorkspaceRevision != 2 || current.Effective.Recovery.MaxAttempts != 1 || current.Effective.Recovery.AgentHours != 2 {
		t.Fatal("workspace tightening was cached", current)
	}
	reset := decode[LeadSettings](t, &owner, "DELETE", path+"?revision=1", "", 200)
	if reset.Revision != 2 || reset.OwnerPersonID == nil || *reset.OwnerPersonID != owner.ID || reset.Effective.WorkKindID != nil || *reset.Effective.Bucket != "normal" {
		t.Fatal("reset lost identity or inheritance", reset)
	}
	expectPrefError(t, owner, "PUT", path, body, 409, "stale_revision")
	expectPrefError(t, otherAdmin, "PUT", path, `{"revision":2,"overrides":{}}`, 403, "owner_required")
	if eventCount(t, owner, "lead.settings_changed") != 4 {
		t.Fatal("rejected writes changed history")
	}
}

func TestLeadSettingsInputAndCurrentBindings(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "lead-bindings", "person", "Owner", []string{"admin"})
	other := addPrincipal(t, owner.TenantID, "person", "Other", []string{"admin"})
	agent := addPrincipal(t, owner.TenantID, "agent", "Agent", nil)
	project := leadProject(t, owner)
	path := "/api/projects/" + project + "/lead-settings"
	account := enrollEvidenceHarness(t, owner, agent, "codex")
	inRegistry(t, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2,linked_at=clock_timestamp() WHERE id=$1`, account, other.ID)
		return err
	})
	expectPrefError(t, owner, "PUT", path, `{"revision":0,"overrides":{"allowed_account_ids":["`+account+`"]}}`, 422, "invalid_owner_binding")
	expectPrefError(t, owner, "PUT", path, `{"revision":0,"overrides":{"allowed_host_ids":["aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"]}}`, 422, "invalid_owner_binding")
	expectPrefError(t, owner, "PUT", path, `{"revision":0,"overrides":{"parallel_limit":100}}`, 400, "invalid_json")
	expectPrefError(t, owner, "PUT", path, `{"revision":0,"overrides":{"recovery":{}}}`, 400, "invalid_json")
	expectPrefError(t, owner, "PUT", path, `{"revision":0,"overrides":{"bucket":"`+strings.Repeat("a", 17<<10)+`"}}`, 400, "invalid_json")
	expectPrefError(t, owner, "PUT", path, `{"overrides":{}}`, 400, "revision_required")
	expectPrefError(t, owner, "PUT", path, `{"revision":0}`, 400, "overrides_required")
	inRegistry(t, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2 WHERE id=$1`, account, owner.ID)
		return err
	})
	saved := decode[LeadSettings](t, &owner, "PUT", path, `{"revision":0,"overrides":{"allowed_account_ids":["`+account+`"]}}`, 200)
	if saved.Effective.AllowedAccountIDs == nil || len(*saved.Effective.AllowedAccountIDs) != 1 {
		t.Fatal(saved)
	}
	inRegistry(t, owner, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='lead.settings_changed' AND (before::text LIKE '%'||$1||'%' OR after::text LIKE '%'||$1||'%')`, account).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("account identifier persisted in public audit")
		}
		return nil
	})

	inRegistry(t, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2 WHERE id=$1`, account, other.ID)
		return err
	})
	relinked := decode[LeadSettings](t, &owner, "GET", path, "", 200)
	if !relinked.DetailsRedacted || relinked.Effective.AllowedAccountIDs != nil || relinked.Overrides != nil {
		t.Fatal("relinked account selectors leaked", relinked)
	}
	inRegistry(t, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='archived' WHERE id=$1`, project)
		return err
	})
	expectPrefError(t, owner, "PUT", path, `{"revision":1,"overrides":{}}`, 409, "project_unavailable")
	inRegistry(t, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='member') WHERE principal_id=$1 AND scope_type='workspace'`, owner.ID)
		return err
	})
	revoked := decode[LeadSettings](t, &other, "GET", path, "", 200)
	if revoked.WaitReason != "owner_unavailable" || revoked.AutomaticLaunchEnabled {
		t.Fatal("revoked owner did not wait", revoked)
	}

}

func TestLeadSettingsWriteRechecksRevocation(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "lead-revoke", "person", "Owner", []string{"admin"})
	project := leadProject(t, owner)
	barrier := &mutationBarrier{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(`{"revision":0,"overrides":{}}`)}
	request := httptest.NewRequest("PUT", "/api/projects/"+project+"/lead-settings", barrier)
	request = request.WithContext(tenant.WithPrincipal(request.Context(), owner))
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	mux := http.NewServeMux()
	New(appPool).Mount(mux)
	go func() { defer close(done); mux.ServeHTTP(recorder, request) }()
	<-barrier.entered
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, owner.TenantID, func(tx pgx.Tx) error {
		if err := preferenceFence(t.Context(), tx, owner); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='member') WHERE principal_id=$1 AND scope_type='workspace'`, owner.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	close(barrier.release)
	<-done
	if recorder.Code != 403 || !strings.Contains(recorder.Body.String(), "permission") {
		t.Fatal("stale authority used", recorder.Code, recorder.Body.String())
	}
	if eventCount(t, owner, "lead.settings_changed") != 0 {
		t.Fatal("revoked settings committed")
	}
	inRegistry(t, owner, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM project_lead_settings`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("rejected mutation persisted")
		}
		return nil
	})
}

func TestLeadSettingsArchivedKindsRemainReadableAndResettable(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		t.Run(fmt.Sprintf("inherited=%v", inherited), func(t *testing.T) {
			reset(t)
			owner := makePrincipal(t, "lead-archived", "person", "Owner", []string{"admin"})
			member := addPrincipal(t, owner.TenantID, "person", "Member", []string{"member"})
			project := leadProject(t, owner)
			path := "/api/projects/" + project + "/lead-settings"
			kindBody := `{"label":"Archived selector"}`
			if !inherited {
				kindBody = `{"label":"Archived selector","project_id":"` + project + `"}`
			}
			kind := decode[workKind](t, &owner, "POST", "/api/work-kinds", kindBody, 201)
			policy := `{"revision":0,"overrides":{"work_kind_id":"` + kind.ID + `"}}`
			if inherited {
				decode[LeadSettings](t, &owner, "PUT", "/api/settings/lead-policy", policy, 200)
				policy = `{"revision":0,"overrides":{"bucket":"complex"}}`
			}
			before := decode[LeadSettings](t, &owner, "PUT", path, policy, 200)
			if before.ModelSelector == nil || before.WaitReason != "acceptance_pending" {
				t.Fatal("fixture lacks a live selector", before)
			}
			decode[workKind](t, &owner, "DELETE", "/api/work-kinds/"+kind.ID, "", 200)
			assertUnavailable := func(view LeadSettings) {
				t.Helper()
				if view.WaitReason != "selector_unavailable" || view.ModelSelector != nil || view.AutomaticLaunchEnabled || view.Effective.WorkKindID == nil || *view.Effective.WorkKindID != kind.ID {
					t.Fatal("archived kind fell back or blocked settings", view)
				}
			}
			current := decode[LeadSettings](t, &owner, "GET", path, "", 200)
			assertUnavailable(current)
			if current.Revision != 1 || current.OwnerPersonID == nil || *current.OwnerPersonID != owner.ID || !slices.Equal(current.ModelRevisions, before.ModelRevisions) {
				t.Fatal("archival changed settings identity or revisions", current)
			}
			public := decode[LeadSettings](t, &member, "GET", path, "", 200)
			if public.WaitReason != "selector_unavailable" || !public.DetailsRedacted || public.Effective.WorkKindID != nil || public.ModelSelector != nil || public.Overrides != nil || public.AutomaticLaunchEnabled {
				t.Fatal("public unavailable state leaked or failed open", public)
			}
			if inherited {
				assertUnavailable(decode[LeadSettings](t, &owner, "GET", "/api/settings/lead-policy", "", 200))
			}
			expectPrefError(t, owner, "PUT", path, `{"revision":1,"overrides":{"work_kind_id":"`+kind.ID+`"}}`, 422, "unknown_kind")
			resetView := decode[LeadSettings](t, &owner, "DELETE", path+"?revision=1", "", 200)
			if resetView.Revision != 2 || resetView.OwnerPersonID == nil || *resetView.OwnerPersonID != owner.ID || resetView.Overrides == nil || resetView.Overrides.WorkKindID != nil || *resetView.Effective.Bucket != "normal" {
				t.Fatal("reset did not preserve ownership and inheritance", resetView)
			}
			if inherited {
				assertUnavailable(resetView)
				decode[workKind](t, &owner, "POST", "/api/work-kinds/"+kind.ID+"/restore", "", 200)
				restored := decode[LeadSettings](t, &owner, "GET", path, "", 200)
				if restored.ModelSelector == nil || restored.WaitReason != "acceptance_pending" || restored.Revision != 2 {
					t.Fatal("restored inherited selector remained unavailable", restored)
				}
			} else if resetView.ModelSelector == nil || resetView.WaitReason != "acceptance_pending" || resetView.Effective.WorkKindID != nil {
				t.Fatal("reset did not clear archived project selector", resetView)
			}
			changes := 2
			if inherited {
				changes++
			}
			if eventCount(t, owner, "lead.settings_changed") != changes {
				t.Fatal("archival/read/rejected write changed settings history")
			}
			persisted := decode[LeadSettings](t, &owner, "GET", path, "", 200)
			if persisted.Revision != 2 || persisted.Overrides == nil || persisted.Overrides.WorkKindID != nil {
				t.Fatal("reset response did not reflect committed settings", persisted)
			}
		})
	}
}

func TestLeadSettingsConcurrentRevisionHasOneWinner(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "lead-race", "person", "Owner", []string{"admin"})
	project := leadProject(t, owner)
	path := "/api/projects/" + project + "/lead-settings"
	mux := http.NewServeMux()
	New(appPool).Mount(mux)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	fence, err := adminPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fence.Rollback(context.Background()) }()
	if _, err := fence.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, owner.TenantID); err != nil {
		t.Fatal(err)
	}
	blocker := fence.Conn().PgConn().PID()
	results := make(chan *httptest.ResponseRecorder, 2)
	for _, bucket := range []string{"normal", "complex"} {
		go func() {
			request := httptest.NewRequest("PUT", path, strings.NewReader(`{"revision":0,"overrides":{"bucket":"`+bucket+`"}}`))
			request = request.WithContext(tenant.WithPrincipal(ctx, owner))
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			results <- recorder
		}()
	}
	// Both handlers must reach the actual transaction fence while it is held.
	// Request-body barriers alone allow sequential database execution.
	for {
		select {
		case r := <-results:
			t.Fatalf("write completed before database contention: %d %s", r.Code, r.Body.String())
		default:
		}
		var waiting int
		if err := adminPool.QueryRow(ctx, `WITH RECURSIVE blocked(pid) AS (
			SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND $1::int=ANY(pg_blocking_pids(pid))
			UNION
			SELECT a.pid FROM pg_stat_activity a JOIN blocked b ON b.pid=ANY(pg_blocking_pids(a.pid)) WHERE a.datname=current_database()
		) SELECT count(*) FROM pg_stat_activity a JOIN blocked b USING(pid)
			WHERE a.wait_event_type='Lock' AND a.query=$2`, blocker, db.TenantFenceSQL).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 2 {
			break
		}
		runtime.Gosched()
	}
	if err := fence.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wins, conflicts := 0, 0
	var winner LeadSettings
	for range 2 {
		var r *httptest.ResponseRecorder
		select {
		case r = <-results:
		case <-ctx.Done():
			t.Fatal("competing writes did not finish", ctx.Err())
		}
		switch r.Code {
		case 200:
			wins++
			if err := json.Unmarshal(r.Body.Bytes(), &winner); err != nil {
				t.Fatal(err)
			}
		case 409:
			var failure struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(r.Body.Bytes(), &failure); err != nil || failure.Code != "stale_revision" {
				t.Fatal(r.Body.String())
			}
			conflicts++
		default:
			t.Fatal(r.Code, r.Body.String())
		}
	}
	if wins != 1 || conflicts != 1 || eventCount(t, owner, "lead.settings_changed") != 1 {
		t.Fatal("competing writes were not serialized", wins, conflicts)
	}
	final := decode[LeadSettings](t, &owner, "GET", path, "", 200)
	if final.Revision != 1 || winner.Revision != 1 || winner.Effective.Bucket == nil || final.Effective.Bucket == nil || *final.Effective.Bucket != *winner.Effective.Bucket {
		t.Fatal(final)
	}
}
