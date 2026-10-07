// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Risks: builder claims opening a gate, false approval and lost project policy.
// Reuse the real review/evidence endpoints; seed a historical same-family
// binding to exercise policy changes without weakening the dispatcher.
func TestReviewPolicyUsesRunIdentityAndLiveProjectOverride(t *testing.T) {
	f := newFixture(t)
	project := testID()
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title,state) SELECT $1,$2,id,'POLICY-1','Review policy','active' FROM node_kinds WHERE slug='project'`, f.person.TenantID, project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.ticket, project)
		return err
	})
	path := "/api/projects/" + project + "/review-policy"
	var settings reviewgate.FamilyPolicySettings
	f.call(t, f.person, "GET", path, nil, 200, &settings)
	if settings.Source != "default" || settings.Policy != nil || settings.Effective.Mode != "other_family" {
		t.Fatal("no-row policy changed existing behaviour")
	}
	run, _ := f.completion(t)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed' WHERE id=$1`, run)
		return err
	})
	in := f.input()
	in.AuthorRunID = &run
	in.AuthorFamily = "anthropic"
	f.call(t, f.agent, "POST", "/api/nodes/"+f.ticket+"/reviews", in, 400, nil)
	in.AuthorFamily = ""
	pr := int64(12)
	in.PullRequest = &pr
	f.m.publisher = &recordingPublisher{}
	var v Review
	f.call(t, f.agent, "POST", "/api/nodes/"+f.ticket+"/reviews", in, 201, &v)
	if v.AuthorFamily != "openai" || v.RunID == nil {
		t.Fatal("author run identity was not bound")
	}
	v = f.seedPolicyReview(t, run, "openai")
	evidence := map[string]any{"kind": "text", "reference": "VERDICT: ok", "run_id": *v.RunID}
	f.call(t, f.agent, "POST", "/api/work-orders/"+v.OrderID+"/evidence", evidence, 403, nil)
	familyOff := reviewgate.FamilyPolicy{Mode: "off", AllowedFamilies: []string{}}
	f.call(t, f.person, "PUT", "/api/settings/review-policy", familyOff, 200, nil)
	f.call(t, f.agent, "POST", "/api/work-orders/"+v.OrderID+"/evidence", evidence, 201, nil)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed' WHERE id=$1`, *v.RunID)
		return err
	})
	check := func(want bool, state, reason string) {
		t.Helper()
		f.tx(t, func(tx pgx.Tx) error {
			var err error
			v, err = load(t.Context(), tx, v.OrderID)
			if err != nil {
				return err
			}
			verified, err := reviewgate.VerifiedLatestTx(t.Context(), tx, v.OrderID, f.ticket)
			if err == nil && verified != want {
				t.Fatalf("verified consumer disagrees with policy: %v, wanted %v", verified, want)
			}
			return err
		})
		if v.GateOpen != want || v.AuthorFamily != "openai" || statusState(v) != state || !strings.Contains(statusDescription(v, state), reason) {
			t.Fatalf("gate %v (%s): %s; recorded author %s", v.GateOpen, statusState(v), v.GateReason, v.AuthorFamily)
		}
	}
	check(true, "success", "policy off")
	f.call(t, f.person, "PUT", path, reviewgate.DefaultFamilyPolicy(), 200, &settings)
	if settings.Source != "project" || settings.Effective.Mode != "other_family" {
		t.Fatal("project override did not beat tenant default")
	}
	check(false, "failure", "not cross-family")
	f.call(t, f.person, "DELETE", path, nil, 200, &settings)
	if settings.Policy != nil || settings.Source != "tenant" || settings.Effective.Mode != "off" {
		t.Fatal("deleting override did not fall back to tenant default")
	}
	check(true, "success", "policy off")
	v = f.seedPolicyReview(t, run, "anthropic")
	evidence["run_id"] = *v.RunID
	f.call(t, f.agent, "POST", "/api/work-orders/"+v.OrderID+"/evidence", evidence, 201, nil)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed' WHERE id=$1`, *v.RunID)
		return err
	})
	f.call(t, f.person, "PUT", path, reviewgate.FamilyPolicy{Mode: "allowlist", AllowedFamilies: []string{"google"}}, 200, nil)
	check(false, "failure", "anthropic not allowed")
	f.call(t, f.person, "PUT", path, reviewgate.FamilyPolicy{Mode: "allowlist", AllowedFamilies: []string{"anthropic"}}, 200, nil)
	check(true, "success", "author openai, reviewer anthropic")
	f.tx(t, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='review_policy.changed' AND before IS NOT NULL AND after IS NOT NULL`).Scan(&count); err != nil {
			return err
		}
		if count != 5 {
			t.Fatalf("policy edits lost audit history: %d", count)
		}
		return nil
	})
}

// New trust boundary: a scoped admin agent key still cannot alter review policy
// without a custom-role grant. The endpoint rechecks the actual key and live
// role; production's separate outer allowlist remains a coordinator dependency.
func TestReviewPolicyExplicitAgentGrantAndTenantIsolation(t *testing.T) {
	f := newFixture(t)
	f.agent.Scopes = append(f.agent.Scopes, "reviewpolicy.read", "reviewpolicy.manage")
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=$2 WHERE principal_id=$1`, f.agent.ID, f.agent.Scopes)
		return err
	})
	put := func(want int) {
		t.Helper()
		var answer map[string]json.RawMessage
		f.call(t, f.agent, "PUT", "/api/settings/review-policy", reviewgate.FamilyPolicy{Mode: "off", AllowedFamilies: []string{}}, want, &answer)
		if want == 403 && string(answer["reason_code"]) != `"missing_role_permission"` {
			t.Fatalf("policy denial did not come from live role authority: %s", answer["error"])
		}
	}
	put(403)
	var role string
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'review_policy_operator','Review policy operator') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'reviewpolicy.manage'),($1,$2,'reviewpolicy.read')`, f.person.TenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1`, f.agent.ID, role)
		return err
	})
	put(200)
	var foreign reviewgate.FamilyPolicySettings
	f.call(t, f.foreign, "GET", "/api/settings/review-policy", nil, 200, &foreign)
	if foreign.Policy != nil || foreign.Effective.Mode != "other_family" {
		t.Fatal("tenant default leaked across tenants")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM cross_family_policies WHERE tenant_id=$1`, f.person.TenantID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("table RLS exposed another tenant's policy")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	projects := []string{testID(), testID()}
	reader := tenant.Principal{ID: testID(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, func(tx pgx.Tx) error {
		for i, project := range projects {
			if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title,state) SELECT $1,$2,id,$3,'Policy isolation','active' FROM node_kinds WHERE slug='project'`, f.person.TenantID, project, []string{"ISOLATION-1", "ISOLATION-2"}[i]); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Project reader')`, reader.TenantID, reader.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE key='member'`, reader.TenantID, reader.ID, projects[0])
		return err
	})
	for _, project := range projects {
		f.call(t, f.person, "PUT", "/api/projects/"+project+"/review-policy", reviewgate.DefaultFamilyPolicy(), 200, nil)
	}
	f.call(t, reader, "GET", "/api/projects/"+projects[0]+"/review-policy", nil, 200, nil)
	f.call(t, reader, "GET", "/api/projects/"+projects[1]+"/review-policy", nil, 404, nil)
	f.call(t, f.foreign, "GET", "/api/projects/"+projects[0]+"/review-policy", nil, 404, nil)
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), reader), f.d.App, reader.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM cross_family_policies WHERE project_id=$1`, projects[1]).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("project RLS exposed another project's policy")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_permissions WHERE role_id=$1 AND permission='reviewpolicy.manage'`, role)
		return err
	})
	put(403)
	f.call(t, f.person, "PUT", "/api/settings/review-policy", map[string]any{"mode": "allowlist", "allowed_families": []string{"invented"}}, 400, nil)
	f.call(t, f.person, "PUT", "/api/settings/review-policy", map[string]any{"mode": "allowlist", "allowed_families": []string{"openai", "openai"}}, 400, nil)
	f.call(t, f.person, "PUT", "/api/settings/review-policy", map[string]any{"mode": "off"}, 400, nil)
	var settings reviewgate.FamilyPolicySettings
	f.call(t, f.person, "GET", "/api/settings/review-policy", nil, 200, &settings)
	if settings.Policy == nil || settings.Policy.Mode != "off" {
		t.Fatal("refused writes altered the policy")
	}
	f.tx(t, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='review_policy.changed'`).Scan(&count); err != nil {
			return err
		}
		if count != 3 {
			t.Fatalf("refused write appended audit event: %d", count)
		}
		return nil
	})
}

type policyPublicationBarrier struct {
	started chan string
	release chan struct{}
}

func (*policyPublicationBarrier) Configured(string, string) bool { return true }
func (b *policyPublicationBarrier) Publish(ctx context.Context, _ string, v Review, state string) (string, error) {
	b.started <- statusDescription(v, state)
	select {
	case <-b.release:
		return state, nil
	case <-ctx.Done():
		return "error", ctx.Err()
	}
}

// Race risk: an old successful network post must not erase a newer policy's
// refresh marker when both decisions have the same status state.
func TestReviewPolicyChangeDuringPublicationRemainsDirty(t *testing.T) {
	f := newFixture(t)
	f.m.publisher = &recordingPublisher{}
	f.call(t, f.person, "PUT", "/api/settings/review-policy", reviewgate.FamilyPolicy{Mode: "off", AllowedFamilies: []string{}}, 200, nil)
	in := f.input()
	pr := int64(12)
	in.PullRequest = &pr
	v := f.completedReview(t, in)
	b := &policyPublicationBarrier{started: make(chan string, 1), release: make(chan struct{})}
	f.m.publisher = b
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	conn, err := f.d.App.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- f.m.publishReview(ctx, conn, f.person.TenantID, v, nil)
	}()
	defer func() { cancel(); <-finished }()
	select {
	case description := <-b.started:
		if !strings.Contains(description, "policy off") {
			t.Fatal("publication did not reach the old-policy barrier")
		}
	case <-ctx.Done():
		t.Fatal("publication never reached barrier")
	}
	f.call(t, f.person, "PUT", "/api/settings/review-policy", reviewgate.DefaultFamilyPolicy(), 200, nil)
	close(b.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("publication did not complete")
	}
	f.tx(t, func(tx pgx.Tx) error {
		var state, reported string
		if err := tx.QueryRow(t.Context(), `SELECT github_status,github_reported_state FROM work_order_reviews WHERE work_order_id=$1`, v.OrderID).Scan(&state, &reported); err != nil {
			return err
		}
		if state != "pending" || reported != "" {
			t.Fatalf("old post erased policy refresh: state=%s, reported=%s", state, reported)
		}
		current, err := load(t.Context(), tx, v.OrderID)
		if err == nil && (!current.GateOpen || !strings.Contains(current.GateReason, "author openai, reviewer anthropic")) {
			t.Fatal("current policy decision was not retained")
		}
		return err
	})
}

func (f *fixture) seedPolicyReview(t *testing.T, authorRun, reviewer string) Review {
	t.Helper()
	var order workorders.Order
	f.call(t, f.person, "POST", "/api/work-orders", workorders.CreateInput{Title: "Historical policy fixture", Parent: &f.ticket, Assignee: &f.agent.ID, Criteria: []string{"Verify recorded provider identity"}}, 201, &order)
	in := f.input()
	run := testID()
	claimedAuthor := "openai"
	if reviewer == "openai" {
		claimedAuthor = "anthropic"
	}
	var v Review
	f.tx(t, func(tx pgx.Tx) error {
		var profile string
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE family=$1 AND version='test-version'`, reviewer).Scan(&profile); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE work_orders SET kind='review',status='running' WHERE node_id=$1`, order.NodeID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,id,work_order_id,agent_principal_id,model_profile_id,account_id,daemon_id,daemon_generation,status,effective_model,model_evidence)
 VALUES($1,$2,$3,$4,$5,$6,'review-daemon','review-generation','running','review-model','vendor_reported')`, f.person.TenantID, run, order.NodeID, f.agent.ID, profile, f.account); err != nil {
			return err
		}
		// Preserve immutable bindings: model a historical incorrect author
		// claim at insertion, never disable guards or rewrite an existing row.
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_order_reviews(tenant_id,work_order_id,ticket_node_id,request_id,request,ticket_snapshot,repository,base_sha,head_sha,author_run_id,author_family,reviewer_profile_id,reviewer_family,run_id,pull_request,github_status)
 VALUES($1,$2,$3,$4,'{}','Historical policy fixture',$5,$6,$7,$8,$9,$10,$11,$12,12,'pending')`, f.person.TenantID, order.NodeID, f.ticket, in.RequestID, in.Repository, in.BaseSHA, in.HeadSHA, authorRun, claimedAuthor, profile, reviewer, run); err != nil {
			return err
		}
		var err error
		v, err = load(t.Context(), tx, order.NodeID)
		return err
	})
	return v
}
