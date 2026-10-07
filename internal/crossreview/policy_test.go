// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/jackc/pgx/v5"
)

// Risks: builder claims opening a gate, false approval and lost project policy.
// Reuse the real review/evidence endpoints; seed a historical same-family
// binding to exercise policy changes without weakening the dispatcher.
func TestReviewPolicyUsesRunIdentityAndLiveProjectOverride(t *testing.T) {
	f := newFixture(t)
	project := testID()
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title) SELECT $1,$2,id,'POLICY-1','Review policy' FROM node_kinds WHERE slug='project'`, f.person.TenantID, project); err != nil {
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
	f.tx(t, func(tx pgx.Tx) error {
		var profile string
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE family='openai' AND version='test-version'`).Scan(&profile); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET model_profile_id=$2,status='running',effective_model='review-model',model_evidence='vendor_reported' WHERE id=$1`, *v.RunID, profile); err != nil {
			return err
		}
		// Historical/client claim is intentionally wrong; the recorded author
		// run remains OpenAI, as does the current reviewer profile.
		_, err := tx.Exec(t.Context(), `UPDATE work_order_reviews SET reviewer_profile_id=$2,reviewer_family='openai',author_family='anthropic' WHERE work_order_id=$1`, v.OrderID, profile)
		return err
	})
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
	f.tx(t, func(tx pgx.Tx) error {
		// Switch the reviewer back to its real Anthropic profile. No author
		// claim changes; the policy must use the recorded OpenAI author.
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET model_profile_id=$2 WHERE id=$1`, *v.RunID, f.profile); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE work_order_reviews SET reviewer_profile_id=$2,reviewer_family='anthropic' WHERE work_order_id=$1`, v.OrderID, f.profile)
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
// without a custom-role grant. Requests go through production authentication;
// revocation is re-evaluated rather than trusting an earlier successful write.
func TestReviewPolicyExplicitAgentGrantAndTenantIsolation(t *testing.T) {
	f := newFixture(t)
	f.agent.Scopes = append(f.agent.Scopes, "reviewpolicy.read", "reviewpolicy.manage")
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=$2 WHERE principal_id=$1`, f.agent.ID, f.agent.Scopes)
		return err
	})
	h := f.boundaryHandler(t, f.d.App)
	put := func(want int) {
		t.Helper()
		r := boundaryRequest(t.Context(), "/api/settings/review-policy", reviewgate.FamilyPolicy{Mode: "off", AllowedFamilies: []string{}}, f.token, nil)
		r.Method = http.MethodPut
		w := serveBoundary(h, r)
		if w.Code != want {
			t.Fatalf("policy PUT: %d, wanted %d: %s", w.Code, want, w.Body.String())
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
		if count != 1 {
			t.Fatalf("refused write appended audit event: %d", count)
		}
		return nil
	})
}
