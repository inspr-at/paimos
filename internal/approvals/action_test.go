// SPDX-License-Identifier: AGPL-3.0-only
package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These helpers run as subtests of the existing ESSENTIAL source/history test.
// Risk: approval could lose exact effects, become a general grant, leak across
// tenants/agents, accept a credential/manual ask, or report delivery prematurely.
// No new test-tier or browser-case ownership is needed.
func actionFixture(t *testing.T) (*fixture, string, ActionInput) {
	t.Helper()
	f := newFixture(t)
	project := insertNode(t, f.db.Admin, f.tenantA, "project", "ACT-1", "Action project")
	f.agentA.Scopes = []string{"approvals.request", "approvals.read", "inbox.read", "inbox.send"}
	f.wide = insertKey(t, f.db.Admin, f.agentA, f.agentA.Scopes, false)
	f.mux = http.NewServeMux()
	New(f.db.App, inbox.RecordActionOutcomeTx).Mount(f.mux)
	inbox.New(f.db.App).Mount(f.mux)
	var now time.Time
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return f, project, ActionInput{IdempotencyKey: "deploy-one", Kind: "deploy", What: "Deploy the approved release", Why: "The release is ready", ExactEffect: "Deploy release digest sha256:fixture-release to the configured production target", Reversibility: "reversible", ReversalPlan: "Redeploy the previously recorded release digest", HumanInput: "none", ExpiresAt: now.Add(time.Hour)}
}

func actionJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func actionRead(t *testing.T, f *fixture, p tenant.Principal, project, id string) ActionRequest {
	t.Helper()
	w := f.do(p, "", "GET", "/api/projects/"+project+"/action-requests/"+id, "")
	if w.Code != 200 {
		t.Fatalf("read action: %d %s", w.Code, w.Body.String())
	}
	var out ActionRequest
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func actionCreate(t *testing.T, f *fixture, project string, in ActionInput) ActionRequest {
	t.Helper()
	w := f.do(f.agentA, f.wide, "POST", "/api/projects/"+project+"/action-requests", actionJSON(t, in))
	if w.Code != 201 {
		t.Fatalf("request action: %d %s", w.Code, w.Body.String())
	}
	var out ActionRequest
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func checkOutwardActionLifecycle(t *testing.T) {
	f, project, in := actionFixture(t)
	ctx := t.Context()
	created := actionCreate(t, f, project, in)
	if created.State != "pending" || created.ApprovalID == nil || len(created.RequestDigestSHA256) != 64 || created.OutcomeReceipt != nil {
		t.Fatalf("pending facts: %+v", created)
	}
	if repeated := actionCreate(t, f, project, in); repeated.ID != created.ID {
		t.Fatal("identical request replay duplicated the approval")
	}
	changed := in
	changed.ExactEffect = "Deploy another revision"
	if w := f.do(f.agentA, f.wide, "POST", "/api/projects/"+project+"/action-requests", actionJSON(t, changed)); w.Code != 409 {
		t.Fatalf("changed exact facts reused key: %d %s", w.Code, w.Body.String())
	}
	w := f.do(f.personA, "", "GET", "/api/approvals/"+*created.ApprovalID, "")
	if w.Code != 200 {
		t.Fatalf("native source: %d %s", w.Code, w.Body.String())
	}
	a := decodeApproval(t, w)
	for _, fact := range []string{in.What, in.Why, in.ExactEffect, in.ReversalPlan, in.Reversibility, f.agentA.ID, project, created.ID, created.RequestDigestSHA256} {
		if !strings.Contains(a.Rationale, fact) {
			t.Fatalf("Desk/phone rationale lost fact %q", fact)
		}
	}
	if a.Risk != "high" || a.Scope != actionScopePrefix+created.RequestDigestSHA256 {
		t.Fatal("exact action scope or high-risk classification missing")
	}
	path := "/api/projects/" + project + "/action-approvals/" + a.ID + "/decision"
	for _, tc := range []struct {
		name, token string
		p           tenant.Principal
		want        int
	}{
		{"requesting agent", f.wide, f.agentA, 403},
		{"other agent", f.tokenB, f.agentB, 403},
		{"other tenant", "", f.personB, 404},
	} {
		if w = f.do(tc.p, tc.token, "POST", path, `{"decision":"approved","reason":"Checked exact target"}`); w.Code != tc.want {
			t.Fatalf("%s decision: %d %s", tc.name, w.Code, w.Body.String())
		}
	}
	if w = f.do(f.personA, "", "POST", path, `{"decision":"approved"}`); w.Code != 400 {
		t.Fatalf("missing reason: %d %s", w.Code, w.Body.String())
	}
	otherProject := insertNode(t, f.db.Admin, f.tenantA, "project", "OTH-1", "Other project")
	if w = f.do(f.personA, "", "POST", "/api/projects/"+otherProject+"/action-approvals/"+a.ID+"/decision", `{"decision":"approved","reason":"Wrong project on screen"}`); w.Code != 404 {
		t.Fatalf("decision crossed its project identity: %d %s", w.Code, w.Body.String())
	}
	// Only a project binding, with no workspace authority. The new adapter
	// reaches the native transaction without widening the old decision route.
	lead := insertPrincipal(t, f.db.Admin, f.tenantA, tenant.Person, "Project lead")
	if _, err := f.db.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
 SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='admin'`, f.tenantA, lead.ID, project); err != nil {
		t.Fatal(err)
	}
	if err := authz.RequirePattern(authz.BindPool(tenant.WithPrincipal(ctx, lead), f.db.App), "POST /api/projects/{projectId}/action-approvals/{approvalId}/decision", authz.Scope{ProjectID: project}); err != nil {
		t.Fatalf("project lead cannot reach action adapter: %v", err)
	}
	if err := authz.RequirePattern(authz.BindPool(tenant.WithPrincipal(ctx, lead), f.db.App), "POST /api/approvals/{approvalId}/decision", authz.Scope{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("existing general decision authorization changed: %v", err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenantA, func(tx pgx.Tx) error {
		can, err := CanNotify(ctx, tx, lead, a.ID)
		if !can && err == nil {
			return fmt.Errorf("authorized project lead is not notification eligible")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w = f.do(lead, "", "POST", path, `{"decision":"approved","reason":"Release digest and configured target checked"}`); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	approved := actionRead(t, f, f.agentA, project, created.ID)
	if approved.State != "approved" || approved.Reason != "Release digest and configured target checked" || approved.DecidedByPrincipalID == nil || *approved.DecidedByPrincipalID != lead.ID || approved.OutcomeReceipt == nil || approved.OutcomeReceipt.State != "queued" || approved.OutcomeReceipt.HandedOffAt != nil {
		t.Fatalf("approval/outcome proof: %+v", approved)
	}
	var grants int
	if err := f.db.Admin.QueryRow(ctx, `SELECT count(*) FROM agent_permission_grants WHERE approval_request_id=$1`, a.ID).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("action approval enlarged a permission: grants=%d err=%v", grants, err)
	}
	messageID := approved.OutcomeReceipt.MessageID
	w = f.do(f.agentA, "", "GET", "/api/inbox/messages?wait_ms=0", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), messageID) || !strings.Contains(w.Body.String(), created.ID) || !strings.Contains(w.Body.String(), "approved") {
		t.Fatalf("agent not informed through inbox: %d %s", w.Code, w.Body.String())
	}
	if w = f.do(f.agentA, "", "POST", "/api/inbox/messages/"+messageID+"/ack", ""); w.Code != 200 {
		t.Fatalf("agent acknowledgment: %d %s", w.Code, w.Body.String())
	}
	if receipt := actionRead(t, f, f.agentA, project, created.ID).OutcomeReceipt; receipt.State != "handed_off" || receipt.HandedOffAt == nil {
		t.Fatalf("receipt did not reflect real acknowledgement: %+v", receipt)
	}
	if w = f.do(lead, "", "POST", path, `{"decision":"denied","reason":"Second outcome"}`); w.Code != 409 {
		t.Fatalf("terminal decision changed: %d %s", w.Code, w.Body.String())
	}
	// Every failure must be the intended authorization failure, not malformed input.
	requestPath := "/api/projects/" + project + "/action-requests"
	missing := f.agentA
	missing.Scopes = []string{"approvals.read"}
	if w = f.do(missing, f.narrow, "POST", requestPath, actionJSON(t, in)); w.Code != 403 || !strings.Contains(w.Body.String(), "API key") {
		t.Fatalf("missing scope: %d %s", w.Code, w.Body.String())
	}
	if w = f.do(f.personA, "", "POST", requestPath, actionJSON(t, in)); w.Code != 403 || !strings.Contains(w.Body.String(), "only an agent") {
		t.Fatalf("person proposed action: %d %s", w.Code, w.Body.String())
	}
	foreignProject := insertNode(t, f.db.Admin, f.tenantB, "project", "FOREIGN-1", "Foreign")
	if w = f.do(f.agentA, f.wide, "POST", "/api/projects/"+foreignProject+"/action-requests", actionJSON(t, in)); w.Code != 404 {
		t.Fatalf("foreign tenant request: %d %s", w.Code, w.Body.String())
	}
	for _, p := range []tenant.Principal{f.agentB, f.personB} {
		if w = f.do(p, "", "GET", requestPath+"/"+created.ID, ""); w.Code != 404 {
			t.Fatalf("foreign read: %d %s", w.Code, w.Body.String())
		}
	}
	if w = f.do(f.agentA, f.wide, "POST", "/api/approvals", proposalJSON(a.Scope, "node", &project, nil)); w.Code != 400 {
		t.Fatalf("raw scope bypass: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.db.Admin.Exec(ctx, `UPDATE action_approval_requests SET exact_effect='different target' WHERE id=$1`, created.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("mutable exact effect: %v", err)
	}
	for _, terminal := range []string{"denied", "expired", "refused"} {
		next := in
		next.IdempotencyKey = terminal
		if terminal == "refused" {
			next.HumanInput = "tokens"
			next.ExactEffect = "Ask the person to provide an API token"
		}
		item := actionCreate(t, f, project, next)
		if terminal == "denied" {
			if w = f.do(lead, "", "POST", "/api/approvals/"+*item.ApprovalID+"/decision", `{"decision":"denied","reason":"Target not authorized"}`); w.Code != 200 {
				t.Fatalf("decline native path: %d %s", w.Code, w.Body.String())
			}
		}
		if terminal == "expired" {
			clock := func(context.Context, pgx.Tx) (time.Time, error) { return next.ExpiresAt.Add(time.Second), nil }
			mod := &Module{pool: f.db.App, inTenant: db.InTenant, actionOutcome: inbox.RecordActionOutcomeTx}
			if worked, err := mod.expireAction(ctx, f.tenantA, clock); err != nil || !worked {
				t.Fatalf("expiry: worked=%v err=%v", worked, err)
			}
			if worked, err := mod.expireAction(ctx, f.tenantA, clock); err != nil || worked {
				t.Fatalf("expiry replay: worked=%v err=%v", worked, err)
			}
			if w = f.do(lead, "", "POST", "/api/approvals/"+*item.ApprovalID+"/decision", `{"decision":"approved","reason":"Too late"}`); w.Code != 409 {
				t.Fatalf("approve expired action: %d %s", w.Code, w.Body.String())
			}
		}
		resolved := actionRead(t, f, f.agentA, project, item.ID)
		if resolved.State != terminal || resolved.Reason == "" || resolved.ResolvedAt == nil || resolved.OutcomeReceipt == nil || resolved.OutcomeReceipt.State != "queued" {
			t.Fatalf("%s outcome: %+v", terminal, resolved)
		}
		if terminal == "refused" && (resolved.ApprovalID != nil || !strings.Contains(resolved.Reason, "prohibited")) {
			t.Fatal("forbidden human ask became a decidable approval")
		}
		if replay := actionCreate(t, f, project, next); replay.ID != resolved.ID || replay.OutcomeReceipt.MessageID != resolved.OutcomeReceipt.MessageID {
			t.Fatal("terminal replay duplicated request or notification")
		}
	}
	// A misleading human_input=none must not bypass recognizable secret asks.
	for i, text := range []string{"Please send me your API key", "Please run this command manually", "Bitte schick mir ein Token"} {
		next := in
		next.IdempotencyKey, next.ExactEffect = fmt.Sprintf("disguised-%d", i), text
		if item := actionCreate(t, f, project, next); item.State != "refused" || item.ApprovalID != nil {
			t.Fatalf("disguised human ask accepted: %+v", item)
		}
	}
	for _, event := range []string{"approval.proposed", "approval.approved", "approval.denied", "action_request.requested", "action_request.approved", "action_request.denied", "action_request.expired", "action_request.refused", "inbox.sent", "inbox.receipt_queued", "inbox.receipt_handed_off"} {
		if !slices.Contains(f.eventTypes(t), event) {
			t.Fatalf("audit missing %s", event)
		}
	}
	var eventFacts ActionRequest
	var raw []byte
	if err := f.db.Admin.QueryRow(ctx, `SELECT after FROM events WHERE type='action_request.approved' AND after->>'id'=$1`, created.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &eventFacts); err != nil || eventFacts.ExactEffect != in.ExactEffect || eventFacts.Reason != approved.Reason || eventFacts.RequestDigestSHA256 != created.RequestDigestSHA256 || eventFacts.OutcomeReceipt == nil || eventFacts.OutcomeReceipt.MessageID != messageID {
		t.Fatalf("incomplete exact-effect decision event: %+v err=%v", eventFacts, err)
	}
	// A queue failure must roll back the decision, outcome and audit together.
	next := in
	next.IdempotencyKey = "outcome-failure"
	atomic := actionCreate(t, f, project, next)
	queueUnavailable := errors.New("injected outcome queue failure")
	mod := &Module{pool: f.db.App, inTenant: db.InTenant, actionOutcome: func(context.Context, pgx.Tx, tenant.Principal, string, string, string, string, string) error {
		return queueUnavailable
	}}
	if _, err := mod.decide(ctx, f.personA, *atomic.ApprovalID, "approved", "Decision must have its outcome receipt"); !errors.Is(err, queueUnavailable) {
		t.Fatalf("queue failure claimed a decision: %v", err)
	}
	if unchanged := actionRead(t, f, f.agentA, project, atomic.ID); unchanged.State != "pending" || unchanged.OutcomeReceipt != nil {
		t.Fatalf("failed outcome delivery partially committed: %+v", unchanged)
	}
	var decisions int
	if err := f.db.Admin.QueryRow(ctx, `SELECT count(*) FROM approval_decisions WHERE request_id=$1`, *atomic.ApprovalID).Scan(&decisions); err != nil || decisions != 0 {
		t.Fatalf("failed outcome persisted decision: count=%d err=%v", decisions, err)
	}
	// Tenant kind overrides are bounded, revisioned and person-only. Disabled
	// kinds cannot approve an already pending request, but can still decline it.
	next = in
	next.IdempotencyKey = "disabled-after-proposal"
	pending := actionCreate(t, f, project, next)
	if w = f.do(f.agentA, f.wide, "PUT", "/api/action-request-kinds/deploy", `{"label":"Deployment","enabled":false,"revision":0}`); w.Code != 403 {
		t.Fatalf("agent configuration: %d %s", w.Code, w.Body.String())
	}
	if w = f.do(f.personA, "", "PUT", "/api/action-request-kinds/deploy", `{"label":"Deployment","enabled":false,"revision":0}`); w.Code != 200 {
		t.Fatalf("kind configuration: %d %s", w.Code, w.Body.String())
	}
	if w = f.do(lead, "", "POST", "/api/approvals/"+*pending.ApprovalID+"/decision", `{"decision":"approved","reason":"Kind disabled"}`); w.Code != 403 || !strings.Contains(w.Body.String(), "disabled") {
		t.Fatalf("disabled kind approved: %d %s", w.Code, w.Body.String())
	}
	if w = f.do(f.personA, "", "PUT", "/api/action-request-kinds/deploy", `{"label":"Deployment","enabled":true,"revision":0}`); w.Code != 409 {
		t.Fatalf("stale kind revision: %d %s", w.Code, w.Body.String())
	}
	if w = f.do(f.personA, "", "GET", "/api/action-request-kinds", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"external_message"`) || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("effective defaults: %d %s", w.Code, w.Body.String())
	}
}

// Risk: a permission revoked after proposal but before the decision transaction
// must win before the final decision write. Channels establish the interleaving;
// the injected transaction boundary is a barrier, not an elapsed-time guess.
func checkOutwardActionAccessFence(t *testing.T) {
	f, project, in := actionFixture(t)
	a := actionCreate(t, f, project, in)
	arrived, proceed := make(chan struct{}), make(chan struct{})
	mod := &Module{pool: f.db.App, inTenant: func(ctx context.Context, pool *pgxpool.Pool, tid string, fn func(pgx.Tx) error) error {
		close(arrived)
		select {
		case <-proceed:
			return db.InTenant(ctx, pool, tid, fn)
		case <-ctx.Done():
			return ctx.Err()
		}
	}, actionOutcome: inbox.RecordActionOutcomeTx}
	finished := make(chan error, 1)
	go func() {
		_, err := mod.decide(t.Context(), f.personA, *a.ApprovalID, "approved", "Access was present before the write")
		finished <- err
	}()
	<-arrived
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantA, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), db.TenantFenceSQL); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.personA.ID)
		return err
	}); err != nil {
		close(proceed)
		t.Fatal(err)
	}
	close(proceed)
	err := <-finished
	var he *httpError
	if !errors.As(err, &he) || he.status != http.StatusForbidden || !strings.Contains(he.msg, "outward action facts are missing") {
		t.Fatalf("revoked decision did not fail on current authority: %v", err)
	}
	var count int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM approval_decisions WHERE request_id=$1`, *a.ApprovalID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked approval committed: count=%d err=%v", count, err)
	}
}
