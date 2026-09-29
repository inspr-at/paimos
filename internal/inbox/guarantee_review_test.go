// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// AEON-280 review round: terminal failure under one lock order, the system
// write path for project-only callers, coordinator privacy, legacy rows.

func projectOnly(t *testing.T, w *world, p tenant.Principal, project string) {
	t.Helper()
	if _, err := w.db.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1::uuid AND principal_id=$2::uuid AND scope_type='workspace'`, p.TenantID, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
		SELECT $1::uuid,$2::uuid,id,'project',$3::uuid FROM roles WHERE tenant_id=$1::uuid AND key='member'`, p.TenantID, p.ID, project); err != nil {
		t.Fatal(err)
	}
}

func boundMessage(t *testing.T, w *world, m *messaging, project, session, key string) CompatMessage {
	t.Helper()
	in := compatInput("codex:worker", key)
	in.RecipientSessionID = &session
	return mustCompatSend(t, m, w.sender, project, in)
}

func TestPullDropsAMessageFailedAfterItWasSelected(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	session := messageTestSession(t, w, project, w.agent, "Racing worker")
	msg := boundMessage(t, w, m, project, session, "race")
	expireDeadline(t, w, msg.ID)
	// The pull has selected the row; the sweeper fails it and commits before the
	// pull locks it. The pull must drop it, not stamp and return it.
	err := db.InTenant(tenant.WithPrincipal(t.Context(), w.agent), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
		var selected string
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM inbox_messages WHERE id=$1::uuid AND acked_at IS NULL`, msg.ID).Scan(&selected); err != nil {
			return err
		}
		if n := sweepNow(t, w, w.sender.TenantID); n != 1 {
			return fmt.Errorf("swept %d", n)
		}
		alive, err := handOver(t.Context(), tx, w.agent, SeenHook, []string{selected})
		if err != nil {
			return err
		}
		if alive[selected] {
			return errors.New("failed message handed over")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var fetched bool
	guaranteeRow(t, w, `SELECT fetched_at IS NOT NULL FROM inbox_messages WHERE id=$1::uuid`, []any{msg.ID}, &fetched)
	if fetched {
		t.Fatal("failed message stamped as delivered")
	}
}

func TestPageStampsOnlyReturnedRows(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	session := messageTestSession(t, w, project, w.agent, "Paged worker")
	first := boundMessage(t, w, m, project, session, "first")
	second := boundMessage(t, w, m, project, session, "second")
	status, body := do(t, srv, w.agent.ID, "GET", "/api/inbox/messages?wait_ms=0&limit=1&session="+session, "", nil)
	if page := mustJSON[Page](t, body); status != 200 || len(page.Items) != 1 || page.Items[0].ID != first.ID {
		t.Fatalf("limit=1 page %d %s", status, body)
	}
	var one, two bool
	guaranteeRow(t, w, `SELECT (SELECT fetched_at IS NOT NULL FROM inbox_messages WHERE id=$1::uuid),(SELECT fetched_at IS NOT NULL FROM inbox_messages WHERE id=$2::uuid)`, []any{first.ID, second.ID}, &one, &two)
	if !one || two {
		t.Fatalf("stamped first=%t second=%t", one, two)
	}
}

// A completion that starts after the adapter injected must not lose to the
// sweeper: a live lease holds off the deadline, and completion and sweep take
// the message row in the same order, so they never deadlock.
func TestCompletionBeatsSweeperUnderConcurrency(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	if _, err := m.storeTarget(t.Context(), w.admin, project, targetInput{Address: "codex:worker", Adapter: "codex", Kind: "codex_thread", Ref: "thread-fixture", Role: "primary", MaximumLevel: "simple"}); err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		msg := mustCompatSend(t, m, w.sender, project, compatInput("codex:worker", fmt.Sprintf("race-%d", i)))
		work, err := m.claim(t.Context(), w.agent, project, claimInput{To: "codex:worker", Adapter: "codex"})
		if err != nil || work == nil || work.Message == nil || work.Message.ID != msg.ID {
			t.Fatalf("claim %d: %+v %v", i, work, err)
		}
		expireDeadline(t, w, msg.ID) // Injected, then the deadline passes.
		var wg sync.WaitGroup
		var completeErr, sweepErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, completeErr = m.complete(t.Context(), w.agent, project, completeInput{ID: work.ID, LeaseToken: work.LeaseToken, EffectiveLevel: "simple"})
		}()
		go func() {
			defer wg.Done()
			_, sweepErr = NewSweeper(w.db.App).SweepTenant(t.Context(), w.sender.TenantID)
		}()
		wg.Wait()
		if completeErr != nil || sweepErr != nil {
			t.Fatalf("round %d: complete %v sweep %v", i, completeErr, sweepErr)
		}
		if state, _ := receiptState(t, w, msg.ID); state != "handed_off" {
			t.Fatalf("round %d: receipt %s", i, state)
		}
	}
}

// Acknowledgement and sweep race on unleased messages: exactly one wins, both
// finish without error or deadlock, and the loser changes nothing.
func TestAckAndSweepAgreeUnderConcurrency(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	session := messageTestSession(t, w, project, w.agent, "Contended worker")
	for i := range 12 {
		msg := boundMessage(t, w, m, project, session, fmt.Sprintf("contended-%d", i))
		expireDeadline(t, w, msg.ID)
		var wg sync.WaitGroup
		var ackErr, sweepErr error
		wg.Add(2)
		go func() { defer wg.Done(); _, ackErr = m.base.ack(context.Background(), w.agent, msg.ID) }()
		go func() {
			defer wg.Done()
			_, sweepErr = NewSweeper(w.db.App).SweepTenant(context.Background(), w.sender.TenantID)
		}()
		wg.Wait()
		if sweepErr != nil {
			t.Fatalf("round %d: sweep %v", i, sweepErr)
		}
		state, _ := receiptState(t, w, msg.ID)
		var acked bool
		guaranteeRow(t, w, `SELECT acked_at IS NOT NULL FROM inbox_messages WHERE id=$1::uuid`, []any{msg.ID}, &acked)
		switch {
		case ackErr == nil && acked && state == "handed_off":
		case errors.Is(ackErr, ErrNotDelivered) && !acked && state == "failed":
		default:
			t.Fatalf("round %d: ack %v acked=%t receipt %s", i, ackErr, acked, state)
		}
	}
}

// Project-only callers write the System events through the narrow SQL path:
// session end and the claim cap must not roll back.
func TestProjectOnlyCallersCanFailMessages(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	session := messageTestSession(t, w, project, w.agent, "Scoped worker")
	msg := boundMessage(t, w, m, project, session, "scoped")
	projectOnly(t, w, w.agent, project)
	// The reason for the narrow path: this caller cannot append a System event.
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), w.agent), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
		sys, err := systemActor(t.Context(), tx, w.agent.TenantID)
		if err != nil {
			return err
		}
		_, err = events.Append(t.Context(), tx, sys, events.Change{Type: "inbox.delivery_failed", After: map[string]string{"probe": "rls"}})
		return err
	}); err == nil {
		t.Fatal("project-only caller appended a System event directly; the test no longer covers the RLS case")
	}
	err := db.InTenant(tenant.WithPrincipal(t.Context(), w.agent), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
		return FailSessionMessages(t.Context(), tx, w.agent.TenantID, session)
	})
	if err != nil {
		t.Fatalf("project-only session end: %v", err)
	}
	if state, reason := receiptState(t, w, msg.ID); state != "failed" || reason != ReasonSessionEnded || len(noticesFor(t, w, msg.ID)) != 1 {
		t.Fatalf("receipt %s %s", state, reason)
	}
	if n := countSQL(t, w.db.Admin, w.sender.TenantID, `SELECT count(*) FROM events e JOIN principals p ON p.id=e.actor_principal_id WHERE p.name='System' AND e.type IN ('inbox.receipt_failed','inbox.delivery_failed') AND e.after->>'message_id'=$1`, msg.ID); n != 2 {
		t.Fatalf("system events %d", n)
	}

	if _, err := w.db.Admin.Exec(t.Context(), `INSERT INTO inbox_delivery_settings(tenant_id,max_attempts) VALUES($1::uuid,1)`, w.agent.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.storeTarget(t.Context(), w.admin, project, targetInput{Address: "codex:worker", Adapter: "codex", Kind: "codex_thread", Ref: "thread-fixture", Role: "primary", MaximumLevel: "simple"}); err != nil {
		t.Fatal(err)
	}
	capped := mustCompatSend(t, m, w.sender, project, compatInput("codex:worker", "capped"))
	if work, err := m.claim(t.Context(), w.agent, project, claimInput{To: "codex:worker", Adapter: "codex"}); err != nil || work == nil {
		t.Fatalf("first claim %+v %v", work, err)
	}
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_message_deliveries SET lease_until=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if work, err := m.claim(t.Context(), w.agent, project, claimInput{To: "codex:worker", Adapter: "codex"}); err != nil || work != nil {
		t.Fatalf("project-only over-cap claim %+v %v", work, err)
	}
	if state, reason := receiptState(t, w, capped.ID); state != "failed" || reason != ReasonAttempts {
		t.Fatalf("capped receipt %s %s", state, reason)
	}
}

// The coordinator copy needs a real coordinator of the recipient's project, and
// quotes the message only when that coordinator runs as the recipient principal.
func TestCoordinatorCopyPrivacy(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	lead := insertPrincipal(t, w.db, w.sender.TenantID, tenant.Agent, "lead", nil)
	child := func(parentPrincipal tenant.Principal, role string) (string, string) {
		parent := messageTestSession(t, w, project, parentPrincipal, "Parent "+role)
		kid := messageTestSession(t, w, project, w.agent, "Kid of "+role)
		if _, err := w.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET role=$2 WHERE id=$1::uuid`, parent, role); err != nil {
			t.Fatal(err)
		}
		if _, err := w.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET parent_id=$2::uuid WHERE id=$1::uuid`, kid, parent); err != nil {
			t.Fatal(err)
		}
		return parent, kid
	}
	fail := func(kid, key string) CompatMessage {
		msg := boundMessage(t, w, m, project, kid, key)
		err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
			failed, err := FailMessage(t.Context(), tx, msg.ID, ReasonDeadline)
			if err == nil && !failed {
				err = errors.New("not failed")
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	// A parent that is only a worker gets nothing.
	_, kid := child(lead, "worker")
	if notices := noticesFor(t, w, fail(kid, "worker-parent").ID); len(notices) != 1 {
		t.Fatalf("non-coordinator parent notified: %+v", notices)
	}
	// Another principal's coordinator learns that, not what.
	_, kid = child(lead, "coordinator")
	msg := fail(kid, "foreign-coordinator")
	if copy := noticesFor(t, w, msg.ID)["/coordinator"]; copy.recipient != lead.ID || strings.Contains(copy.body, msg.Body) || !strings.Contains(copy.body, msg.ID) {
		t.Fatalf("foreign coordinator copy %+v", copy)
	}
	// A coordinator running as the recipient principal already has access.
	parent, kid := child(w.agent, "coordinator")
	msg = fail(kid, "own-coordinator")
	if copy := noticesFor(t, w, msg.ID)["/coordinator"]; copy.recipient != w.agent.ID || copy.session == nil || *copy.session != parent || !strings.Contains(copy.body, msg.Body) {
		t.Fatalf("own coordinator copy %+v", copy)
	}
}

// Messages accepted before deadlines existed keep their old behaviour
// everywhere: session end, the claim cap and FailMessage leave them alone.
func TestLegacyMessagesAreExempt(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	session := messageTestSession(t, w, project, w.agent, "Legacy worker")
	legacy := boundMessage(t, w, m, project, session, "legacy")
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_receipts SET deliver_by=NULL WHERE message_id=$1::uuid`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		if err := FailSessionMessages(t.Context(), tx, w.sender.TenantID, session); err != nil {
			return err
		}
		failed, err := FailMessage(t.Context(), tx, legacy.ID, ReasonDeadline)
		if err == nil && failed {
			err = errors.New("legacy message failed")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if state, _ := receiptState(t, w, legacy.ID); state != "queued" || len(noticesFor(t, w, legacy.ID)) != 0 {
		t.Fatalf("legacy receipt %s", state)
	}
	if _, err := w.db.Admin.Exec(t.Context(), `INSERT INTO inbox_delivery_settings(tenant_id,max_attempts) VALUES($1::uuid,1)`, w.agent.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.storeTarget(t.Context(), w.admin, project, targetInput{Address: "codex:worker", Adapter: "codex", Kind: "codex_thread", Ref: "thread-fixture", Role: "primary", MaximumLevel: "simple"}); err != nil {
		t.Fatal(err)
	}
	old := mustCompatSend(t, m, w.sender, project, compatInput("codex:worker", "old-adapter"))
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_receipts SET deliver_by=NULL WHERE message_id=$1::uuid`, old.ID); err != nil {
		t.Fatal(err)
	}
	for round := range 2 {
		work, err := m.claim(t.Context(), w.agent, project, claimInput{To: "codex:worker", Adapter: "codex"})
		if err != nil || work == nil || work.Message == nil || work.Message.ID != old.ID {
			t.Fatalf("legacy claim %d: %+v %v", round, work, err)
		}
		if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_message_deliveries SET lease_until=now()-interval '1 second'`); err != nil {
			t.Fatal(err)
		}
	}
	if state, _ := receiptState(t, w, old.ID); state != "queued" {
		t.Fatalf("legacy adapter receipt %s", state)
	}
}

// Legacy acknowledged-but-queued receipts are healed once, in bounded batches.
func TestLegacyHealIsOneTime(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	stale := mustCompatSend(t, m, w.sender, project, compatInput("codex:worker", "stale-legacy"))
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_receipts SET deliver_by=NULL WHERE message_id=$1::uuid`, stale.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_messages SET acked_at=now(),acked_by_principal_id=recipient_principal_id WHERE id=$1::uuid`, stale.ID); err != nil {
		t.Fatal(err)
	}
	sweeper := NewSweeper(w.db.App)
	if _, err := sweeper.SweepTenant(t.Context(), w.sender.TenantID); err != nil {
		t.Fatal(err)
	}
	if state, _ := receiptState(t, w, stale.ID); state != "handed_off" || !sweeper.legacyHealed[w.sender.TenantID] {
		t.Fatalf("legacy heal %s done=%t", state, sweeper.legacyHealed[w.sender.TenantID])
	}
}
