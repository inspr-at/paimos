// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Risk: coordinators forge approvals from unrelated sessions; capabilities
// cross runs/ranges, survive revocation or policy off, or bless legacy rows.
// Exercise the real production authentication boundary and store transactions.
func TestAuthenticatedReviewerRuns(t *testing.T) {
	reset(t)
	ctx := t.Context()
	tid := insertTenant(t, "review-runs", "Reviewer runs")
	m := newMod(t, Config{Env: "prod"})
	owner := tenant.Principal{TenantID: tid, Kind: tenant.Person}
	if err := adminPool.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Dispatcher owner') RETURNING id::text`, tid).Scan(&owner.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, testDB, tid, owner.ID, "owner")
	txdo := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := testInTenant(ctx, appPool, tid, fn); err != nil {
			t.Fatal(err)
		}
	}
	key := func(name string, scopes []string) keyRecord {
		t.Helper()
		k, err := m.createAgentKey(ctx, owner, name, "", scopes, nil)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	coordinator := key("dispatcher", append(append([]string{}, authz.CoordinatorKeyScopes...), "nodes.write"))
	author := key("author", []string{"nodes.read", "nodes.write"})
	reviewer := key("reviewer", []string{"nodes.read"})
	other := key("other-reviewer", []string{"nodes.read"})
	var project, ticket, authorSession, reviewerSession, otherSession, sameSession string
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	txdo(func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'RR-1','Reviewer runs' FROM node_kinds WHERE slug='project' RETURNING id::text`, tid).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,parent_id,key,title,fields) SELECT $1,id,$2,'RR-2','Bound approval','{"code":true,"ui":true,"hide_from_release_notes":true}' FROM node_kinds WHERE slug='work' RETURNING id::text`, tid, project).Scan(&ticket); err != nil {
			return err
		}
		for _, s := range []struct {
			principal, harness, model string
			id                        *string
		}{{author.PrincipalID, "codex", "gpt-6.1-sol", &authorSession}, {reviewer.PrincipalID, "claude", "claude-opus-4-6", &reviewerSession}, {other.PrincipalID, "claude", "claude-opus-4-6", &otherSession}, {other.PrincipalID, "codex", "gpt-6.1-sol", &sameSession}} {
			digest := sha256.Sum256([]byte(s.principal + s.harness))
			if err := tx.QueryRow(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,model,reasoning_effort,host,management,role,work_shape,ref_digest,lease_digest)
 VALUES($1,$2,$3,$4,$5,$6,'xhigh','fixture','unmanaged','worker','ship',$7,$7) RETURNING id::text`, tid, project, s.principal, ticket, s.harness, s.model, digest[:]).Scan(s.id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository,ci_workflow,nightly_workflow,updated_at) VALUES($1,$2,'owner/repo','ci.yml','nightly.yml',now())`, tid, project); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO delivery_items(tenant_id,id,project_id,ticket_node_id,repository,pull_request,head_sha,state,state_since,owner,observation,updated_at)
 VALUES($1,gen_random_uuid(),$2,$3,'owner/repo',7,$4,'built',now(),'fixture',jsonb_build_object('head_sha',$4::text,'base_sha',$5::text,'target_branch','main'),now())`, tid, project, ticket, head, base)
		return err
	})
	mux := http.NewServeMux()
	nodes.New(appPool, nil).Mount(mux)
	// Governance setup uses the actual definition handler; subsequent reads
	// and status writes pass through real bearer authentication.
	definition := httptest.NewRequest("PUT", "/api/projects/"+project+"/definitions", strings.NewReader(`{"expected_revision":null,"checks":{"done":[{"id":"review_ok_cross_family","condition":"all"},{"id":"visual_ok","condition":"all"}]}}`))
	definition = definition.WithContext(tenant.WithPrincipal(ctx, owner))
	definitionResponse := httptest.NewRecorder()
	mux.ServeHTTP(definitionResponse, definition)
	if definitionResponse.Code != 200 {
		t.Fatalf("definition setup: %d", definitionResponse.Code)
	}
	secured := m.Middleware(mux)
	request := func(token, capability, method, path string, body any, want int) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if capability != "" {
			r.Header.Set(reviewgate.CredentialHeader, capability)
		}
		_, r.Pattern = mux.Handler(r)
		w := httptest.NewRecorder()
		secured.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d (credential response omitted)", method, path, w.Code, want)
		}
		return w.Body.Bytes()
	}
	checkDone := func(want int, legacy bool) {
		t.Helper()
		raw := request(reviewer.Token, "", "GET", "/api/nodes/"+ticket+"/entry-checks", nil, 200)
		var checks workorders.EntryChecks
		if json.Unmarshal(raw, &checks) != nil {
			t.Fatal("invalid entry checks")
		}
		for _, block := range checks.Blocks {
			if block.ToStatus != "done" {
				continue
			}
			if block.Total != 2 || block.Passed != want {
				t.Fatalf("Done checks = %d/%d, want %d/2", block.Passed, block.Total, want)
			}
			if legacy {
				for _, check := range block.Checks {
					if !strings.Contains(check.Reason, "legacy_unverified") {
						t.Fatal("Done did not flag legacy evidence")
					}
				}
			}
			return
		}
		t.Fatal("Done block missing")
	}
	input := reviewgate.RunCreate{Repository: "owner/repo", PR: 7, Target: "main", Base: base, Head: head, Scope: base + ".." + head, AuthorSession: authorSession, ReviewerSession: reviewerSession, Kind: "review"}
	create := func(in reviewgate.RunCreate) reviewgate.Run {
		t.Helper()
		raw := request(coordinator.Token, "", "POST", "/api/nodes/"+ticket+"/review-runs", in, 201)
		var run reviewgate.Run
		if json.Unmarshal(raw, &run) != nil || run.ID == "" || strings.Contains(string(raw), "credential") {
			t.Fatal("dispatcher did not receive a credential-free run")
		}
		return run
	}
	claim := func(k keyRecord, run reviewgate.Run) string {
		t.Helper()
		raw := request(k.Token, "", "POST", "/api/review-runs/"+run.ID+"/credential", nil, 200)
		var out struct {
			Credential string `json:"credential"`
		}
		if json.Unmarshal(raw, &out) != nil || reviewgate.CredentialTenant(out.Credential) != tid {
			t.Fatal("assigned reviewer did not receive a valid private capability")
		}
		return out.Credential
	}
	inputVerdict := func(run reviewgate.Run) reviewgate.RunSubmit {
		return reviewgate.RunSubmit{Binding: run.RunBinding, Verdict: "ok", VisualImpact: "no", Reason: "Presentation unchanged; exact range reviewed."}
	}
	check := func(kind string, want bool) {
		t.Helper()
		txdo(func(tx pgx.Tx) error {
			got, err := reviewgate.AuthenticatedOKTx(ctx, tx, ticket, kind)
			if err == nil && got != want {
				t.Fatalf("%s evidence = %v want %v", kind, got, want)
			}
			return err
		})
	}
	run := create(input)
	request(coordinator.Token, "", "POST", "/api/review-runs/"+run.ID+"/verdict", inputVerdict(run), 403)
	request(coordinator.Token, "", "POST", "/api/review-runs/"+run.ID+"/credential", nil, 403)
	request(other.Token, "", "POST", "/api/review-runs/"+run.ID+"/credential", nil, 403)
	var revision time.Time
	if err := adminPool.QueryRow(ctx, `SELECT updated_at FROM nodes WHERE id=$1`, ticket).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	request(coordinator.Token, "", "POST", "/api/nodes/"+ticket+"/entry-evidence", map[string]any{"check_id": "review_ok_cross_family", "ok": true, "expected_revision": revision, "head_sha": head, "base_sha": base, "author_session_id": authorSession, "reviewer_session_id": reviewerSession}, 403)
	capability := claim(reviewer, run)
	request(reviewer.Token, capability, "POST", "/api/review-runs/"+run.ID+"/verdict", inputVerdict(run), 403)
	another := create(input)
	request("", capability, "POST", "/api/review-runs/"+another.ID+"/verdict", inputVerdict(another), 401)
	for _, field := range []string{"head", "scope", "target", "ticket", "repo", "pr"} {
		bad := inputVerdict(run)
		switch field {
		case "head":
			bad.Binding.Head = strings.Repeat("c", 40)
			bad.Binding.Scope = base + ".." + bad.Binding.Head
		case "scope":
			bad.Binding.Base = strings.Repeat("d", 40)
			bad.Binding.Scope = bad.Binding.Base + ".." + head
		case "target":
			bad.Binding.Target = "release"
		case "ticket":
			bad.Binding.Ticket = project
		case "repo":
			bad.Binding.Repository = "other/repo"
		case "pr":
			bad.Binding.PR = 8
		}
		request("", capability, "POST", "/api/review-runs/"+run.ID+"/verdict", bad, 409)
	}
	spoof := map[string]any{"binding": run.RunBinding, "verdict": "ok", "visual_impact": "no", "reason": "fixture", "model": "gpt-6.1-sol"}
	request("", capability, "POST", "/api/review-runs/"+run.ID+"/verdict", spoof, 400)
	raw := request("", capability, "POST", "/api/review-runs/"+run.ID+"/verdict", inputVerdict(run), 200)
	var accepted reviewgate.Run
	if json.Unmarshal(raw, &accepted) != nil || accepted.Model != "claude-opus-4-6" || accepted.Harness != "claude" || accepted.Effort != "xhigh" || accepted.AuthorFamily != "openai" || accepted.ReviewerFamily != "anthropic" || accepted.Verdict != "ok" {
		t.Fatal("verdict was not attributed from the immutable run")
	}
	check("review", true)
	checkDone(1, false)
	request(author.Token, "", "PATCH", "/api/nodes/"+ticket, map[string]any{"state": "done"}, 409)
	request("", capability, "POST", "/api/review-runs/"+run.ID+"/verdict", inputVerdict(run), 401)
	// Store boundary and injected clock prove expiry without sleeps or editing
	// the immutable binding. The same capability remains usable at real time.
	expiring := create(input)
	expiringCap := claim(reviewer, expiring)
	txdo(func(tx pgx.Tx) error {
		_, err := reviewgate.CredentialPrincipalTx(ctx, tx, expiring.ID, expiringCap, expiring.ExpiresAt)
		if !errors.Is(err, reviewgate.ErrRunCredential) {
			t.Fatalf("expiry rejected for wrong reason: %v", err)
		}
		return nil
	})
	// A retarget invalidates already accepted evidence and an unsubmitted run.
	txdo(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE delivery_items SET observation=jsonb_set(observation,'{target_branch}','"release"') WHERE ticket_node_id=$1`, ticket)
		return err
	})
	check("review", false)
	request("", expiringCap, "POST", "/api/review-runs/"+expiring.ID+"/verdict", inputVerdict(expiring), 409)
	txdo(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE delivery_items SET observation=jsonb_set(observation,'{target_branch}','"main"') WHERE ticket_node_id=$1`, ticket)
		return err
	})
	// Policy off cannot create same-family Done/admission evidence.
	txdo(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO cross_family_policies(tenant_id,mode,allowed_families,updated_by,updated_at) VALUES($1,'off','{}',$2,now())`, tid, owner.ID)
		return err
	})
	same := input
	same.ReviewerSession = sameSession
	request(coordinator.Token, "", "POST", "/api/nodes/"+ticket+"/review-runs", same, 409)
	// Visual grant is checked both at claim and inside the final write.
	visualInput := input
	visualInput.Kind = "visual"
	visual := create(visualInput)
	request(reviewer.Token, "", "POST", "/api/review-runs/"+visual.ID+"/credential", nil, 403)
	// Explicit custom grants on both key owner and reviewer; no built-in role
	// gains the visual grant. The dispatcher retains its independent binding.
	var reviewerRole string
	txdo(func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT role_id::text FROM role_bindings WHERE principal_id=$1 AND scope_type='workspace'`, reviewer.PrincipalID).Scan(&reviewerRole); err != nil {
			return err
		}
		var personRole string
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'visual_owner','Explicit visual owner') RETURNING id::text`, tid).Scan(&personRole); err != nil {
			return err
		}
		permissions := []string{"reviews.visual_approve"}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest($3::text[])`, tid, personRole, permissions); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, tid, owner.ID, personRole, project); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'reviews.visual_approve')`, tid, reviewerRole); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE agent_keys SET scopes=array_append(scopes,'reviews.visual_approve') WHERE id=$1`, reviewer.ID)
		return err
	})
	visualCap := claim(reviewer, visual)
	txdo(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE role_id=$1 AND permission='reviews.visual_approve'`, reviewerRole)
		return err
	})
	request("", visualCap, "POST", "/api/review-runs/"+visual.ID+"/verdict", inputVerdict(visual), 403)
	txdo(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'reviews.visual_approve')`, tid, reviewerRole)
		return err
	})
	request("", visualCap, "POST", "/api/review-runs/"+visual.ID+"/verdict", inputVerdict(visual), 200)
	check("visual", true)
	checkDone(2, false)
	request(author.Token, "", "PATCH", "/api/nodes/"+ticket, map[string]any{"state": "done"}, 200)
	// Existing rows are retained and visibly flagged; a latest legacy approval
	// cannot replace an authenticated run in either evidence consumer.
	txdo(func(tx pgx.Tx) error {
		for _, kind := range []string{"coordinator_review.recorded", "entry_check.evidence"} {
			body, _ := json.Marshal(map[string]any{"check_id": "visual_ok", "ok": true, "head_sha": head, "base_sha": base, "author_family": "openai", "reviewer_family": "anthropic"})
			if err := (nodes.SQLWriter{}).WriteEvent(ctx, tx, nodes.Event{ActorPrincipalID: owner.ID, NodeID: &ticket, Type: kind, After: body}); err != nil {
				return err
			}
		}
		return nil
	})
	check("review", false)
	check("visual", false)
	checkDone(0, true)
	raw = request(reviewer.Token, "", "GET", "/api/nodes/"+ticket+"/review-evidence", nil, 200)
	var provenance reviewgate.EvidenceProvenance
	if json.Unmarshal(raw, &provenance) != nil || !provenance.ReviewLegacy || !provenance.VisualLegacy {
		t.Fatal("existing rows were not flagged legacy_unverified")
	}
	// Live key revocation fails credential lookup, rather than consuming it.
	txdo(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE agent_keys SET revoked_at=now() WHERE id=$1`, reviewer.ID)
		return err
	})
	request("", expiringCap, "POST", "/api/review-runs/"+expiring.ID+"/verdict", inputVerdict(expiring), 401)
	// Tenant-only capability storage never crosses the authenticated tenant.
	foreign := insertTenant(t, "foreign-review-runs", "Other tenant")
	if err := db.InTenant(dbtest.Seed(context.Background()), appPool, foreign, func(tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM review_runs WHERE tenant_id=$1`, tid).Scan(&n)
		if err == nil && n != 0 {
			t.Fatal("review runs leaked across tenant RLS")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
