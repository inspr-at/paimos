// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/assistants"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Risk: the caller's budget authorizes a hosted send, a denied message remains
// readable, or a policy revocation after acceptance still reaches the provider.
func TestAssistantAdapterPolicyBeforeAcceptanceAndHandoff(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	_, err := m.storeTarget(t.Context(), w.admin, project, targetInput{Address: "grok_bot:worker", Adapter: "grok_bot_routine", Kind: "https_webhook", Ref: "https://8.8.8.8/policy-fixture", Secret: "synthetic-adapter-fixture", Role: "primary", MaximumLevel: "simple"})
	if err != nil {
		t.Fatal(err)
	}
	policy := assistants.Policy{AllowedProjects: []string{project}, AllowedScopes: []string{"inbox.read", "inbox.send"}, WeeklyBudget: assistants.Budget{Amount: "1", Unit: "USD"}, OutwardActionRule: "approval_required", NeverAskPersonForSecretsOrManualSteps: true}
	put := func() {
		t.Helper()
		err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE principals SET agent_class='assistant' WHERE id=$1`, w.agent.ID); err != nil {
				return err
			}
			raw, _ := json.Marshal(policy)
			_, err := tx.Exec(t.Context(), `INSERT INTO assistant_profiles(tenant_id,principal_id,owner_person_id,vendor,policy) VALUES($1,$2,$3,'grok',$4) ON CONFLICT(tenant_id,principal_id) DO UPDATE SET policy=excluded.policy,revision=assistant_profiles.revision+1`, w.agent.TenantID, w.agent.ID, w.admin.ID, raw)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	put()
	first, err := m.commitMessage(t.Context(), w.sender, project, compatInput("grok_bot:worker", "policy-first"))
	if err != nil {
		t.Fatal(err)
	}
	denied, err := m.commitMessage(t.Context(), w.sender, project, compatInput("grok_bot:worker", "policy-over-budget"))
	if err == nil || !strings.Contains(err.Error(), "assistant_weekly_budget_exhausted") || denied.ID == "" {
		t.Fatalf("send refusal: %v", err)
	}
	if state, reason := receiptState(t, w, denied.ID); state != "failed" || reason != "assistant_weekly_budget_exhausted" {
		t.Fatalf("refused receipt %s %s", state, reason)
	}
	page, err := m.base.pending(t.Context(), w.agent, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range page {
		if msg.ID == denied.ID {
			t.Fatal("denied work was readable")
		}
	}
	// Revoke after acceptance. The final claim, before any network attempt,
	// must fail the first message and preserve the exact refusal reason.
	policy.AllowedScopes = []string{"inbox.send"}
	put()
	calls := 0
	dispatcher := &RoutineDispatcher{m: m, client: &http.Client{Transport: routineRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}}
	if _, err := dispatcher.DispatchOne(t.Context(), w.agent.TenantID); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("revoked adapter made a network call")
	}
	if state, reason := receiptState(t, w, first.ID); state != "failed" || reason != "assistant_scope_denied: inbox.read" {
		t.Fatalf("revoked receipt %s %s", state, reason)
	}
	policy.AllowedScopes = []string{"inbox.read", "inbox.send"}
	policy.WeeklyBudget.Amount = "2"
	put()
	next, err := m.commitMessage(t.Context(), w.sender, project, compatInput("grok_bot:worker", "policy-next"))
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := dispatcher.DispatchOne(t.Context(), w.agent.TenantID); err != nil || !worked || calls != 1 {
		t.Fatalf("allowed dispatch worked=%v calls=%d err=%v", worked, calls, err)
	}
	if state, _ := receiptState(t, w, next.ID); state != "handed_off" {
		t.Fatalf("handoff state=%s", state)
	}
	if _, err := dispatcher.DispatchOne(t.Context(), w.agent.TenantID); err != nil || calls != 1 {
		t.Fatal("handoff replay")
	}
	if countSQL(t, w.db.App, w.agent.TenantID, `SELECT count(*) FROM assistant_budget_reservations WHERE principal_id=$1`, w.agent.ID) != 2 {
		t.Fatal("send/claim charged more than once")
	}
	// Scope revocation also applies to pull pickup after work was queued.
	if _, err := m.storeTarget(t.Context(), w.admin, project, targetInput{Address: "grok_bot:worker", Adapter: "grok_bot_routine", Kind: "pull", Role: "primary", MaximumLevel: "simple"}); err != nil {
		t.Fatal(err)
	}
	pull, err := m.commitMessage(t.Context(), w.sender, project, compatInput("grok_bot:worker", "policy-pull"))
	if err != nil {
		t.Fatal(err)
	}
	policy.AllowedProjects = []string{}
	put()
	if page, err := m.base.pending(t.Context(), w.agent, 0, 100); err == nil || len(page) != 0 || !strings.Contains(err.Error(), "assistant_project_denied") {
		t.Fatalf("revoked pull: count=%d err=%v", len(page), err)
	}
	if countSQL(t, w.db.App, w.agent.TenantID, `SELECT count(*) FROM events WHERE type='inbox.message_fetched' AND after->>'message_id'=$1`, pull.ID) != 0 {
		t.Fatal("denied pull was marked fetched")
	}
}
