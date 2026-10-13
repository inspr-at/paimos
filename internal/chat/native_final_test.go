// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"context"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: making a final-only drain use a stopped writer could accidentally relax
// the ordinary final route or leak its body into legacy inbox/wake machinery.
// The native helper is called only inside the independently authorized/fenced
// native transaction; the existing route continues denying the stopped lease.
func TestNativeFinalStorageKeepsStoppedLeaseAndChatIsolation(t *testing.T) {
	f := newFixture(t)
	f.grantSend(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "native-final"))
	session, lease := f.sessionWith(t, f.agent.ID, f.alice, f.project, "worker", "managed", "chat")
	f.bind(t, f.alice, thread, session, "0")
	f.stopNative(t, session)
	proof := WorkerBindingRequest{thread.ID, session, "1"}
	expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/final", finalSend{proof, outboxSend{ClientID: "stopped-legacy", Body: "must not persist"}}, lease), 404)
	b := nativecontract.Binding{TenantID: f.agent.TenantID, ProjectID: f.project, OwnedSessionID: uid(), HarnessSessionID: session, RuntimeEpoch: uid(), DaemonID: "fixture", DaemonGeneration: "fixture", BindingRevision: 1}
	final := nativecontract.FinalPublish{RequestID: uid(), ExpectedBinding: b, FinalID: uid(), InputID: uid(), TurnRef: "opaque-turn", Text: "private native final canary", DrainGrantID: uid()}
	target := NativeFinalTarget{OwnerPersonID: f.alice.ID, ConversationID: thread.ID, RoleID: thread.Role.ID, SessionID: session}
	ctx := tenant.WithPrincipal(context.Background(), f.agent)
	var first NativeFinalWrite
	for i := 0; i < 2; i++ {
		var write NativeFinalWrite
		err := db.InTenant(ctx, f.d.App, f.agent.TenantID, func(tx pgx.Tx) error {
			if err := db.LockTree(ctx, tx, f.agent.TenantID); err != nil {
				return err
			}
			var err error
			write, err = f.m.StoreNativeFinalTx(ctx, tx, f.agent, target, final)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if write.Receipt.State != "persisted" || write.Receipt.MessageID == nil {
			t.Fatalf("native final %+v", write.Receipt)
		}
		if i == 0 {
			first = write
		} else if *write.Receipt.MessageID != *first.Receipt.MessageID {
			t.Fatal("native final retry duplicated message")
		}
	}
	var count, legacy, wakes, deliveries int
	err := f.d.Admin.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM inbox_messages WHERE tenant_id=$1 AND chat_thread_id=$2 AND body=$3),
 (SELECT count(*) FROM inbox_messages WHERE tenant_id=$1 AND chat_thread_id IS NULL AND body=$3),
 (SELECT count(*) FROM inbox_wakes WHERE tenant_id=$1),
 (SELECT count(*) FROM harness_deliveries WHERE tenant_id=$1)`, f.agent.TenantID, thread.ID, final.Text).Scan(&count, &legacy, &wakes, &deliveries)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || legacy != 0 || wakes != 0 || deliveries != 0 {
		t.Fatalf("storage privacy: messages=%d legacy=%d wakes=%d deliveries=%d", count, legacy, wakes, deliveries)
	}
	for _, reader := range []tenant.Principal{f.bob, f.admin, f.foreign} {
		expect(t, f.call(reader, "GET", "/api/chat-threads/"+thread.ID+"/outbox", nil, ""), 404)
	}
	page := decode[outboxPage](t, f.call(f.alice, "GET", "/api/chat-threads/"+thread.ID+"/outbox", nil, ""))
	if len(page.Items) != 1 || page.Items[0].Message.Body != final.Text || page.Items[0].Receipt.State != "sent" {
		t.Fatalf("authorized native final history: %+v", page)
	}
	expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/final", finalSend{proof, outboxSend{ClientID: "stopped-legacy", Body: final.Text}}, lease), 404)
}
