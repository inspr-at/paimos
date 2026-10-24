// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/assistants"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
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
	// Only a granted run consumes allowance; transport acceptance is free.
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(t.Context(), tx, w.agent.TenantID); err != nil {
			return err
		}
		d, err := assistants.EvaluateTx(t.Context(), tx, assistants.Check{TenantID: w.agent.TenantID, PrincipalID: w.agent.ID, ProjectID: project, Operation: "routine.admit", Key: "reserved-run", Charge: true, Ceiling: &policy.WeeklyBudget})
		if err != nil {
			return err
		}
		if !d.Allowed {
			t.Fatalf("run reservation refused: %s", d.Reason)
		}
		return assistants.AppendDecisionsTx(t.Context(), tx, w.admin, d)
	}); err != nil {
		t.Fatal(err)
	}
	denied, err := m.commitMessage(t.Context(), w.sender, project, compatInput("grok_bot:worker", "policy-over-budget"))
	if err == nil || !strings.Contains(err.Error(), "assistant_weekly_budget_exhausted") || denied.ID == "" {
		t.Fatalf("send refusal: %v", err)
	}
	if state, reason := receiptState(t, w, denied.ID); state != "failed" || reason != "assistant_weekly_budget_exhausted" {
		t.Fatalf("refused receipt %s %s", state, reason)
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
	if page, err := m.base.pending(t.Context(), w.agent, 0, 100); err != nil || len(page) != 0 {
		t.Fatalf("denied work was readable: count=%d err=%v", len(page), err)
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
		t.Fatal("expected one run hold and one hosted message charge")
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
	if page, err := m.base.pending(t.Context(), w.agent, 0, 100); err != nil || len(page) != 0 {
		t.Fatalf("revoked pull: count=%d err=%v", len(page), err)
	}
	if state, reason := receiptState(t, w, pull.ID); state != "failed" || reason != "assistant_project_denied" {
		t.Fatalf("revoked pull receipt=%s reason=%s", state, reason)
	}
	if countSQL(t, w.db.App, w.agent.TenantID, `SELECT count(*) FROM events WHERE type='inbox.message_fetched' AND after->>'message_id'=$1`, pull.ID) != 0 {
		t.Fatal("denied pull was marked fetched")
	}
}

func assistantProjectPage(t *testing.T, m *messaging, p tenant.Principal, project string) compatPage {
	t.Helper()
	mux := http.NewServeMux()
	m.Mount(mux)
	r := httptest.NewRequest("GET", "/api/projects/"+project+"/messages/listen", nil)
	r = r.WithContext(tenant.WithPrincipal(t.Context(), p))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("listen: %d %s", w.Code, w.Body.String())
	}
	var page compatPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

// B1: project listening must use the same per-message grant refusal as inbox
// pickup, preserve permitted work, and notify the rejected message's sender.
func TestAssistantProjectListenRechecksPickupPolicy(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	policy := assistants.DefaultPolicy()
	policy.AllowedProjects = []string{project}
	policy.WeeklyBudget.Amount = "1"
	putMessageAssistantPolicy(t, w, policy)
	denied := mustCompatSend(t, m, w.sender, project, compatInput(w.agent.ID, "stale-routine"))
	allowed := mustCompatSend(t, m, w.sender, project, compatInput(w.agent.ID, "ordinary-message"))
	// A queued assignment whose grant disappeared is refused by pendingVia.
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_messages SET idempotency_key='routine-run/'||gen_random_uuid()::text WHERE id=$1`, denied.ID); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		page := assistantProjectPage(t, m, w.agent, project)
		if len(page.Items) != 1 || page.Items[0].ID != allowed.ID || page.NextAfter != allowed.SentEventID {
			t.Errorf("project pickup exposed rejected work or lost allowed work: %+v", page)
		}
	}
	if state, reason := receiptState(t, w, denied.ID); state != "failed" || reason != "assistant_routine_grant_changed" {
		t.Errorf("grant refusal receipt=%s reason=%s", state, reason)
	}
	if got := countSQL(t, w.db.App, w.agent.TenantID, `SELECT count(*) FROM assistant_policy_receipts WHERE principal_id=$1 AND operation='adapter.pull' AND NOT allowed AND reason='assistant_routine_grant_changed'`, w.agent.ID); got != 1 {
		t.Errorf("policy refusal receipts=%d want 1", got)
	}
	notices := noticesFor(t, w, denied.ID)
	if len(notices) != 1 || notices[""].recipient != w.sender.ID || !strings.Contains(notices[""].body, "Not delivered") {
		t.Errorf("sender notice=%+v", notices)
	}
	if got := countSQL(t, w.db.App, w.agent.TenantID, `SELECT count(*) FROM events WHERE type='inbox.message_fetched' AND after->>'message_id'=$1`, denied.ID); got != 0 {
		t.Errorf("rejected message fetched=%d", got)
	}
}

// B1: a valid key and unchanged project grant cannot outlive the assistant's
// owner. The project route must keep the same refusal and sender notice.
func TestAssistantProjectListenRejectsInactiveOwner(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	policy := assistants.DefaultPolicy()
	policy.AllowedProjects, policy.WeeklyBudget.Amount = []string{project}, "1"
	putMessageAssistantPolicy(t, w, policy)
	msg := mustCompatSend(t, m, w.sender, project, compatInput(w.agent.ID, "inactive-owner"))
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE principals SET status='deactivated' WHERE id=$1`, w.admin.ID); err != nil {
		t.Fatal(err)
	}
	if page := assistantProjectPage(t, m, w.agent, project); len(page.Items) != 0 || page.NextAfter != msg.SentEventID {
		t.Fatalf("ownerless pickup: %+v", page)
	}
	if state, reason := receiptState(t, w, msg.ID); state != "failed" || reason != "assistant_owner_unavailable" {
		t.Fatalf("owner refusal receipt=%s %s", state, reason)
	}
	if notices := noticesFor(t, w, msg.ID); len(notices) != 1 || notices[""].recipient != w.sender.ID {
		t.Fatalf("owner refusal notices=%+v", notices)
	}
}

// B2: both transport modes must consume a finite allowance, and repeated
// pickup must not charge an already admitted message again.
func TestAssistantMessageBudgetCoversPushAndPull(t *testing.T) {
	for _, mode := range []string{"push", "inbox", "project"} {
		t.Run(mode, func(t *testing.T) {
			w, m, project, _ := messagingWorld(t)
			target := targetInput{Address: "grok_bot:worker", Adapter: "grok_bot_routine", Kind: "pull", Role: "primary", MaximumLevel: "simple"}
			if mode == "push" {
				target.Kind, target.Ref, target.Secret = "https_webhook", "https://8.8.8.8/policy-fixture", "synthetic-adapter-fixture"
			}
			if _, err := m.storeTarget(t.Context(), w.admin, project, target); err != nil {
				t.Fatal(err)
			}
			policy := assistants.DefaultPolicy()
			policy.AllowedProjects, policy.WeeklyBudget.Amount = []string{project}, "0.02"
			putMessageAssistantPolicy(t, w, policy)
			var messages []CompatMessage
			for _, key := range []string{"one", "two", "three"} {
				messages = append(messages, mustCompatSend(t, m, w.sender, project, compatInput(target.Address, key)))
			}
			if mode == "push" {
				calls := 0
				d := &RoutineDispatcher{m: m, client: &http.Client{Transport: routineRoundTrip(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
				})}}
				for range messages {
					if _, err := d.DispatchOne(t.Context(), w.agent.TenantID); err != nil {
						t.Fatal(err)
					}
				}
				if calls != 2 {
					t.Errorf("provider calls=%d want 2", calls)
				}
			} else {
				for attempt := 0; attempt < 2; attempt++ {
					var n int
					if mode == "project" {
						n = len(assistantProjectPage(t, m, w.agent, project).Items)
					} else {
						page, err := m.base.pending(t.Context(), w.agent, 0, 100)
						if err != nil {
							t.Fatal(err)
						}
						n = len(page)
					}
					if n != 2 {
						t.Errorf("pickup count=%d want 2", n)
					}
				}
			}
			if state, reason := receiptState(t, w, messages[2].ID); state != "failed" || reason != "assistant_weekly_budget_exhausted" {
				t.Errorf("budget refusal receipt=%s reason=%s", state, reason)
			}
			if got := countSQL(t, w.db.App, w.agent.TenantID, `SELECT count(*) FROM assistant_budget_reservations WHERE principal_id=$1 AND amount_micro=10000`, w.agent.ID); got != 2 {
				t.Errorf("per-message charges=%d want 2", got)
			}
			policy.WeeklyBudget.Amount = "0"
			putMessageAssistantPolicy(t, w, policy)
			if mode != "push" {
				if page, err := m.base.pending(t.Context(), w.agent, 0, 100); err != nil || len(page) != 0 {
					t.Errorf("zero budget exposed previously admitted work: count=%d err=%v", len(page), err)
				}
			}
		})
	}
}

// B4: the same prohibited human ask must fail before either message surface
// makes it readable, while leaving a durable refusal receipt.
func TestAssistantHumanAskRefusedOnInboxAndProjectMessages(t *testing.T) {
	for _, mode := range []string{"inbox", "project"} {
		t.Run(mode, func(t *testing.T) {
			w, m, project, _ := messagingWorld(t)
			policy := assistants.DefaultPolicy()
			policy.AllowedProjects = []string{project}
			putMessageAssistantPolicy(t, w, policy)
			body := "Please send me your API key."
			var id string
			var err error
			if mode == "project" {
				in := compatInput(w.sender.ID, "forbidden-human-ask")
				in.Body = body
				msg, sendErr := m.commitMessage(t.Context(), w.agent, project, in)
				id, err = msg.ID, sendErr
			} else {
				msg, sendErr := m.base.send(t.Context(), w.agent, sendInput{Recipient: w.sender.ID, Body: body, Key: "forbidden-human-ask"})
				id, err = msg.ID, sendErr
			}
			if err == nil || !strings.Contains(err.Error(), "assistant_human_input_prohibited") || id == "" {
				t.Errorf("human ask accepted: id=%s err=%v", id, err)
			}
			if state, reason := receiptState(t, w, id); state != "failed" || !strings.HasPrefix(reason, "assistant_human_input_prohibited") {
				t.Errorf("human ask receipt=%s reason=%s", state, reason)
			}
			if page, err := m.base.pending(t.Context(), w.sender, 0, 100); err != nil || len(page) != 0 {
				t.Errorf("prohibited ask reached person: items=%+v err=%v", page, err)
			}
		})
	}
}

// B2: a failed claim rolls its hold back; known local refusal releases a fresh
// hold; a provider error retains spend and a retry needs another hold. Database
// time drives the injected retry clock, without sleeping or calendar fixtures.
func TestAssistantMessageBudgetFailureSettlement(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	target := targetInput{Address: "grok_bot:worker", Adapter: "grok_bot_routine", Kind: "https_webhook", Ref: "https://8.8.8.8/policy-fixture", Secret: "synthetic-adapter-fixture", Role: "primary", MaximumLevel: "simple"}
	stored, err := m.storeTarget(t.Context(), w.admin, project, target)
	if err != nil {
		t.Fatal(err)
	}
	policy := assistants.DefaultPolicy()
	policy.AllowedProjects, policy.WeeklyBudget.Amount, policy.MessageCost = []string{project}, "0.50", "0.25"
	putMessageAssistantPolicy(t, w, policy)
	first := mustCompatSend(t, m, w.sender, project, compatInput(target.Address, "failed-claim"))
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_message_targets SET sealed_target=decode('00','hex') WHERE id=$1`, stored.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.claim(t.Context(), w.agent, project, claimInput{To: target.Address, Adapter: target.Adapter}); err == nil || err.Error() != "target decryption failed" {
		t.Fatalf("wrong pre-exposure failure: %v", err)
	}
	if got := countSQL(t, w.db.App, w.agent.TenantID, `SELECT count(*) FROM assistant_budget_reservations WHERE principal_id=$1`, w.agent.ID); got != 0 {
		t.Fatalf("aborted claim retained %d holds", got)
	}
	if state, reason := receiptState(t, w, first.ID); state != "queued" || reason != "" {
		t.Fatalf("aborted claim receipt=%s %s", state, reason)
	}
	// A decryptable but invalid private address fails after claim, before any
	// socket. Only that fresh push hold may be released by the trusted server.
	nonce := make([]byte, m.aead.NonceSize())
	sealed := m.aead.Seal(nonce, nonce, []byte(`{"ref":"https://127.0.0.1/fixture","secret":"synthetic"}`), []byte(w.agent.TenantID+"/"+project+"/"+stored.ID))
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_message_targets SET sealed_target=$2 WHERE id=$1`, stored.ID, sealed); err != nil {
		t.Fatal(err)
	}
	calls := 0
	d := &RoutineDispatcher{m: m, client: &http.Client{Transport: routineRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 500, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}}
	if worked, err := d.DispatchOne(t.Context(), w.agent.TenantID); !worked || err == nil || err.Error() != "routine target unavailable" {
		t.Fatalf("local refusal worked=%v err=%v", worked, err)
	}
	if calls != 0 || countSQL(t, w.db.App, w.agent.TenantID, `SELECT count(*) FROM assistant_budget_reservations WHERE principal_id=$1`, w.agent.ID) != 0 {
		t.Fatal("local refusal spent allowance or contacted provider")
	}
	if got := countSQL(t, w.db.App, w.agent.TenantID, `SELECT count(*) FROM events WHERE type='assistant.message_hold_released' AND after->>'principal_id'=$1`, w.agent.ID); got != 1 {
		t.Fatalf("release audit=%d", got)
	}
	if _, err := m.storeTarget(t.Context(), w.admin, project, target); err != nil {
		t.Fatal(err)
	}
	message := mustCompatSend(t, m, w.sender, project, compatInput(target.Address, "uncertain-provider"))
	var now time.Time
	if err := w.db.Admin.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	now, _ = assistants.Week(now, policy)
	now = now.Add(time.Hour) // every retry remains inside this policy week
	m.databaseClock = func(context.Context, pgx.Tx) (time.Time, error) { return now, nil }
	for attempt := 0; attempt < 2; attempt++ {
		if worked, err := d.DispatchOne(t.Context(), w.agent.TenantID); !worked || err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("provider failure worked=%v err=%v", worked, err)
		}
		now = now.Add(time.Minute)
	}
	if _, err := d.DispatchOne(t.Context(), w.agent.TenantID); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || countSQL(t, w.db.App, w.agent.TenantID, `SELECT count(*) FROM assistant_budget_reservations WHERE principal_id=$1 AND amount_micro=250000`, w.agent.ID) != 2 {
		t.Fatalf("provider retry bypassed configured cost: calls=%d", calls)
	}
	if state, reason := receiptState(t, w, message.ID); state != "failed" || reason != "assistant_weekly_budget_exhausted" {
		t.Fatalf("retry budget receipt=%s %s", state, reason)
	}
}

// B2: an item that becomes ineligible between hold and handoff must not leave
// a charge for an unexposed page. The trigger deterministically expires it at
// the reservation write; no timing threshold or sleep establishes this case.
func TestAssistantPullBudgetRollsBackWhenHandoffChanges(t *testing.T) {
	for _, route := range []string{"inbox", "project"} {
		t.Run(route, func(t *testing.T) {
			w, m, project, _ := messagingWorld(t)
			policy := assistants.DefaultPolicy()
			policy.AllowedProjects, policy.WeeklyBudget.Amount = []string{project}, "1"
			putMessageAssistantPolicy(t, w, policy)
			msg := mustCompatSend(t, m, w.sender, project, compatInput(w.agent.ID, "expiry-during-pickup"))
			if _, err := w.db.Admin.Exec(t.Context(), `CREATE FUNCTION expire_pickup_fixture() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN UPDATE inbox_messages SET expires_at=clock_timestamp()-interval '1 microsecond'
 WHERE id::text=split_part(NEW.operation_key,'/',2); RETURN NEW; END $$;
 CREATE TRIGGER expire_pickup_fixture AFTER INSERT ON assistant_budget_reservations
 FOR EACH ROW EXECUTE FUNCTION expire_pickup_fixture()`); err != nil {
				t.Fatal(err)
			}
			if route == "inbox" {
				items, err := m.base.pending(t.Context(), w.agent, 0, 100)
				if err == nil || !strings.Contains(err.Error(), "retry pickup") || len(items) != 0 {
					t.Fatalf("changed pickup: items=%+v err=%v", items, err)
				}
			} else {
				mux := http.NewServeMux()
				m.Mount(mux)
				r := httptest.NewRequest("GET", "/api/projects/"+project+"/messages/listen", nil)
				r = r.WithContext(tenant.WithPrincipal(t.Context(), w.agent))
				out := httptest.NewRecorder()
				mux.ServeHTTP(out, r)
				if out.Code != 409 || !strings.Contains(out.Body.String(), "pickup_changed") {
					t.Fatalf("changed project pickup: %d %s", out.Code, out.Body.String())
				}
			}
			for _, query := range []string{
				`SELECT count(*) FROM assistant_budget_reservations WHERE principal_id=$1`,
				`SELECT count(*) FROM assistant_policy_receipts WHERE principal_id=$1 AND operation='adapter.pull'`,
				`SELECT count(*) FROM inbox_messages WHERE recipient_principal_id=$1 AND fetched_at IS NOT NULL`,
			} {
				if got := countSQL(t, w.db.App, w.agent.TenantID, query, w.agent.ID); got != 0 {
					t.Fatalf("aborted pull left state: %s = %d", query, got)
				}
			}
			if state, reason := receiptState(t, w, msg.ID); state != "queued" || reason != "" {
				t.Fatalf("aborted pull receipt=%s %s", state, reason)
			}
			if _, err := w.db.Admin.Exec(t.Context(), `DROP TRIGGER expire_pickup_fixture ON assistant_budget_reservations`); err != nil {
				t.Fatal(err)
			}
			if items, err := m.base.pending(t.Context(), w.agent, 0, 100); err != nil || len(items) != 1 || items[0].ID != msg.ID {
				t.Fatalf("rollback lost original message: %+v err=%v", items, err)
			}
		})
	}
}

func putMessageAssistantPolicy(t *testing.T, w *world, policy assistants.Policy) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE principals SET agent_class='assistant' WHERE id=$1`, w.agent.ID); err != nil {
			return err
		}
		raw, _ := json.Marshal(policy)
		_, err := tx.Exec(t.Context(), `INSERT INTO assistant_profiles(tenant_id,principal_id,owner_person_id,vendor,policy) VALUES($1,$2,$3,'grok',$4) ON CONFLICT(tenant_id,principal_id) DO UPDATE SET policy=excluded.policy,revision=assistant_profiles.revision+1`, w.agent.TenantID, w.agent.ID, w.admin.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// Risk: message processing is free or takes the entire week's allowance.
// Fixed per-message charges share the allowance with person-granted runs.
func TestAssistantAdapterMessagesChargeBudget(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	if _, err := m.storeTarget(t.Context(), w.admin, project, targetInput{Address: "grok_bot:worker", Adapter: "grok_bot_routine", Kind: "https_webhook", Ref: "https://8.8.8.8/policy-fixture", Secret: "synthetic-adapter-fixture", Role: "primary", MaximumLevel: "simple"}); err != nil {
		t.Fatal(err)
	}
	policy := assistants.Policy{AllowedProjects: []string{project}, AllowedScopes: []string{"inbox.read", "inbox.send"}, WeeklyBudget: assistants.Budget{Amount: "1", Unit: "USD"}, OutwardActionRule: "approval_required", NeverAskPersonForSecretsOrManualSteps: true}
	putMessageAssistantPolicy(t, w, policy)
	calls := 0
	dispatcher := &RoutineDispatcher{m: m, client: &http.Client{Transport: routineRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}}
	for _, key := range []string{"first-send", "second-send"} {
		msg, err := m.commitMessage(t.Context(), w.sender, project, compatInput("grok_bot:worker", key))
		if err != nil {
			t.Fatalf("message %s should fit per-message allowance: %v", key, err)
		}
		if worked, err := dispatcher.DispatchOne(t.Context(), w.agent.TenantID); err != nil || !worked {
			t.Fatalf("handoff %s: worked=%v err=%v", key, worked, err)
		}
		if state, reason := receiptState(t, w, msg.ID); state != "handed_off" || reason != "" {
			t.Fatalf("message %s receipt=%s reason=%s", key, state, reason)
		}
	}
	if calls != 2 || countSQL(t, w.db.App, w.agent.TenantID, `SELECT count(*) FROM assistant_budget_reservations WHERE principal_id=$1 AND amount_micro=10000`, w.agent.ID) != 2 {
		t.Fatalf("message transport did not charge per-message budget; provider calls=%d", calls)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(t.Context(), tx, w.agent.TenantID); err != nil {
			return err
		}
		d, err := assistants.EvaluateTx(t.Context(), tx, assistants.Check{TenantID: w.agent.TenantID, PrincipalID: w.agent.ID, ProjectID: project, Operation: "routine.admit", Key: "granted-run", Charge: true, Ceiling: &assistants.Budget{Amount: "0.98", Unit: "USD"}})
		if err != nil {
			return err
		}
		if !d.Allowed {
			t.Fatalf("routine could not reserve the remaining allowance: %s", d.Reason)
		}
		return assistants.AppendDecisionsTx(t.Context(), tx, w.admin, d)
	}); err != nil {
		t.Fatal(err)
	}
	denied, err := m.commitMessage(t.Context(), w.sender, project, compatInput("grok_bot:worker", "spent-run-budget"))
	if err == nil || !strings.Contains(err.Error(), "assistant_weekly_budget_exhausted") || denied.ID == "" {
		t.Fatalf("exhausted run budget must refuse new messages: id=%s err=%v", denied.ID, err)
	}
	if state, reason := receiptState(t, w, denied.ID); state != "failed" || reason != "assistant_weekly_budget_exhausted" {
		t.Fatalf("budget refusal receipt=%s reason=%s", state, reason)
	}
}

// Risk: one newly forbidden project blocks permitted work in the same page,
// while the sender is still told that the rejected message is queued.
func TestAssistantPullRefusalDoesNotStarveAllowedMessages(t *testing.T) {
	w, m, revoked, _ := messagingWorld(t)
	if _, err := w.db.Admin.Exec(t.Context(), `INSERT INTO inbox_delivery_targets(tenant_id,principal_id,kind,webhook_url) VALUES($1,$2,'webhook','https://fixture.invalid/wake')`, w.sender.TenantID, w.sender.ID); err != nil {
		t.Fatal(err)
	}
	allowed := peerTestProject(t, w, w.admin, "ALLOWED")
	for _, project := range []string{revoked, allowed} {
		if _, err := m.storeTarget(t.Context(), w.admin, project, targetInput{Address: "grok_bot:worker", Adapter: "grok_bot_routine", Kind: "pull", Role: "primary", MaximumLevel: "simple"}); err != nil {
			t.Fatal(err)
		}
	}
	policy := assistants.Policy{AllowedProjects: []string{revoked, allowed}, AllowedScopes: []string{"inbox.read", "inbox.send"}, WeeklyBudget: assistants.Budget{Amount: "1", Unit: "USD"}, OutwardActionRule: "approval_required", NeverAskPersonForSecretsOrManualSteps: true}
	putMessageAssistantPolicy(t, w, policy)
	denied := mustCompatSend(t, m, w.sender, revoked, compatInput("grok_bot:worker", "revoked-project"))
	permitted := mustCompatSend(t, m, w.sender, allowed, compatInput("grok_bot:worker", "allowed-project"))
	policy.AllowedProjects = []string{allowed}
	putMessageAssistantPolicy(t, w, policy)
	for attempt := 0; attempt < 2; attempt++ {
		page, err := m.base.pending(t.Context(), w.agent, 0, 100)
		if err != nil || len(page) != 1 || page[0].ID != permitted.ID {
			t.Fatalf("mixed pull page must return permitted message: items=%+v err=%v", page, err)
		}
	}
	if state, reason := receiptState(t, w, denied.ID); state != "failed" || reason != "assistant_project_denied" {
		t.Fatalf("rejected pull receipt=%s reason=%s", state, reason)
	}
	if state, reason := receiptState(t, w, permitted.ID); state != "queued" || reason != "" {
		t.Fatalf("unacknowledged allowed receipt=%s reason=%s", state, reason)
	}
	notices := noticesFor(t, w, denied.ID)
	sender, ok := notices[""]
	if len(notices) != 1 || !ok || sender.recipient != w.sender.ID || sender.session != nil ||
		!strings.Contains(sender.body, "Not delivered") || !strings.Contains(sender.body, denied.ID) ||
		!strings.Contains(sender.body, "assistant project denied") || !strings.Contains(sender.body, denied.Body) {
		t.Fatalf("policy refusal must send exactly one standard System notice: %+v", notices)
	}
	if got := countSQL(t, w.db.App, w.agent.TenantID, `SELECT count(*) FROM inbox_wakes w JOIN inbox_messages m ON m.tenant_id=w.tenant_id AND m.id=w.message_id WHERE m.idempotency_key=$1 AND m.recipient_principal_id=$2::uuid AND m.sent_event_id IS NOT NULL`, "delivery-failed/"+denied.ID, w.sender.ID); got != 1 {
		t.Fatalf("sender notice wakes=%d want 1", got)
	}
	for _, kind := range []string{"inbox.receipt_failed", "inbox.delivery_failed"} {
		if got := countSQL(t, w.db.App, w.agent.TenantID, `SELECT count(*) FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id WHERE e.type=$1 AND e.after->>'message_id'=$2 AND p.name='System' AND 'system'=ANY(p.roles) AND coalesce(e.after->>'failure_reason',e.after->>'reason')='assistant_project_denied'`, kind, denied.ID); got != 1 {
			t.Fatalf("system %s for policy refusal=%d want 1", kind, got)
		}
	}
	for _, check := range []struct {
		kind, id string
		want     int
	}{{"inbox.message_fetched", denied.ID, 0}, {"inbox.receipt_failed", denied.ID, 1}, {"inbox.message_fetched", permitted.ID, 1}} {
		if got := countSQL(t, w.db.App, w.agent.TenantID, `SELECT count(*) FROM events WHERE type=$1 AND after->>'message_id'=$2`, check.kind, check.id); got != check.want {
			t.Fatalf("%s for %s=%d want %d", check.kind, check.id, got, check.want)
		}
	}
}
