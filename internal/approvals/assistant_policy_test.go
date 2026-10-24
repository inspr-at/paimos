// SPDX-License-Identifier: AGPL-3.0-only
package approvals

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/assistants"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: assistant policy is skipped for exact outward facts or a person outside
// its approver ceiling can grant consent. No approved effect is executed here.
func TestAssistantOutwardPolicyApproversAndReceipts(t *testing.T) {
	f, project, in := actionFixture(t)
	policy := assistants.Policy{AllowedProjects: []string{project}, AllowedScopes: []string{"approvals.request", "approvals.read", "inbox.read", "inbox.send"}, WeeklyBudget: assistants.Budget{Amount: "1", Unit: "USD"}, OutwardActionRule: "approval_required", NeverAskPersonForSecretsOrManualSteps: true}
	put := func() {
		t.Helper()
		raw, _ := json.Marshal(policy)
		if _, err := f.db.Admin.Exec(t.Context(), `UPDATE principals SET agent_class='assistant' WHERE id=$1`, f.agentA.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO assistant_profiles(tenant_id,principal_id,owner_person_id,vendor,policy) VALUES($1,$2,$3,'grok',$4) ON CONFLICT(tenant_id,principal_id) DO UPDATE SET policy=excluded.policy,revision=assistant_profiles.revision+1`, f.tenantA, f.agentA.ID, f.personA.ID, raw); err != nil {
			t.Fatal(err)
		}
	}
	put()
	request := actionCreate(t, f, project, in)
	if request.State != "pending" || request.ApprovalID == nil {
		t.Fatal("outward request skipped approval")
	}
	// A positive-budget, scoped ordinary inbox send has no outward approval:
	// consent is required for exact effects, not for transport of stored data.
	if w := f.do(f.agentA, f.wide, "POST", "/api/inbox/messages", `{"recipient_principal_id":"`+f.personA.ID+`","body":"Stored action status","idempotency_key":"policy-status"}`); w.Code != 201 {
		t.Fatalf("ordinary send: %d %s", w.Code, w.Body.String())
	}
	approver := insertPrincipal(t, f.db.Admin, f.tenantA, tenant.Person, "Designated approver")
	dbtest.BindRole(t, f.db, f.tenantA, approver.ID, "admin")
	policy.ApproverPersonIDs = []string{approver.ID}
	put()
	if w := f.do(f.personA, "", "POST", "/api/approvals/"+*request.ApprovalID+"/decision", `{"decision":"approved","reason":"Owner tries outside approver list"}`); w.Code != 403 {
		t.Fatalf("non-approver: %d", w.Code)
	}
	if w := f.do(f.agentA, f.wide, "POST", "/api/approvals/"+*request.ApprovalID+"/decision", `{"decision":"approved","reason":"Assistant attempts self approval"}`); w.Code != 403 {
		t.Fatalf("assistant self approval: %d", w.Code)
	}
	if w := f.do(approver, "", "POST", "/api/approvals/"+*request.ApprovalID+"/decision", `{"decision":"approved","reason":"Designated person approved exact effect"}`); w.Code != 200 {
		t.Fatalf("designated approval: %d %s", w.Code, w.Body.String())
	}
	policy.ApproverPersonIDs = nil
	put()
	in.IdempotencyKey = "default-owner"
	request = actionCreate(t, f, project, in)
	if w := f.do(f.personA, "", "POST", "/api/approvals/"+*request.ApprovalID+"/decision", `{"decision":"approved","reason":"Exact effect approved"}`); w.Code != 200 {
		t.Fatalf("owner approval: %d %s", w.Code, w.Body.String())
	}
	got := actionRead(t, f, f.personA, project, request.ID)
	if got.State != "approved" || got.OutcomeReceipt == nil || got.OutcomeReceipt.State != "queued" {
		t.Fatal("approved receipt missing")
	}
	policy.AllowedScopes = []string{"inbox.read", "inbox.send"}
	put()
	in.IdempotencyKey = "scope-denied"
	denied := actionCreate(t, f, project, in)
	if denied.State != "refused" || denied.ApprovalID != nil || denied.Reason != "assistant_scope_denied: approvals.request" || denied.OutcomeReceipt == nil {
		t.Fatalf("scope refusal: %+v", denied)
	}
	policy.AllowedScopes = []string{"approvals.request", "approvals.read", "inbox.read", "inbox.send"}
	policy.WeeklyBudget.Amount = "0"
	put()
	in.IdempotencyKey = "budget-denied"
	denied = actionCreate(t, f, project, in)
	if denied.State != "refused" || denied.Reason != "assistant_weekly_budget_exhausted" || denied.OutcomeReceipt == nil {
		t.Fatal("zero-budget action request was not refused")
	}
	policy.WeeklyBudget.Amount = "1"
	put()
	in.IdempotencyKey = "policy-revoked-before-decision"
	pending := actionCreate(t, f, project, in)
	policy.AllowedProjects = []string{}
	put()
	w := f.do(f.personA, "", "POST", "/api/approvals/"+*pending.ApprovalID+"/decision", `{"decision":"approved","reason":"Policy changed since proposal"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"denied"`) {
		t.Fatalf("stale policy approval: %d %s", w.Code, w.Body.String())
	}
	got = actionRead(t, f, f.personA, project, pending.ID)
	if got.State != "denied" || got.Reason != "assistant_project_denied" || got.OutcomeReceipt == nil {
		t.Fatal("policy refusal did not reach action history/receipt")
	}
}
