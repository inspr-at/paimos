package agentmessage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/db"
)

func TestMessageBodyProductionGuardPreservesBytesAndRejectsBeforeLedgerWrite(t *testing.T) {
	svc, project, agents := openEnvelopeSecurityDB(t, "sender", "receiver")
	if _, err := db.DB.Exec(`INSERT INTO agent_message_allowlist(receiver_agent_id,sender_agent_id) VALUES(?,?)`, agents["receiver"], agents["sender"]); err != nil {
		t.Fatal(err)
	}
	body := "  observation\r\n\r\nsecond line\r\n"
	message, err := svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{ProjectID: project, Sender: "sender", To: "codex:receiver", Body: body, ExpectsReply: true, IdempotencyKey: "valid-multiline"})
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.DB.QueryRow(`SELECT body FROM agent_messages WHERE message_id=?`, message.MessageID).Scan(&stored); err != nil || stored != body {
		t.Fatalf("body changed: %q err=%v", stored, err)
	}
	counts := func() [4]int {
		t.Helper()
		var got [4]int
		for i, table := range []string{"agent_messages", "agent_message_deliveries", "agent_message_idempotency", "agent_reply_obligations"} {
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got[i]); err != nil {
				t.Fatal(err)
			}
		}
		return got
	}
	before := counts()
	for _, unsafe := range []string{"ordinary\x00hidden\n", "Bearer\nabcdefgh1234", "api_\nkey: example", "ghp_abcdefghij\nklmnopqrstuvwxyz", "-----BE\nGIN PRIVATE KEY-----"} {
		_, err := svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{ProjectID: project, Sender: "sender", To: "codex:receiver", Body: unsafe, ExpectsReply: true, IdempotencyKey: "rejected-multiline"})
		if !errors.Is(err, agentmessage.ErrContainsSecret) {
			t.Fatalf("expected typed secret refusal, got %v", err)
		}
		if got := counts(); got != before {
			t.Fatalf("rejected body changed durable ledger: before=%v after=%v", before, got)
		}
	}
	_, err = svc.SendMessage(context.Background(), agents["sender"], agents["receiver"], nil, nil, "legacy\x00hidden\n")
	if !errors.Is(err, agentmessage.ErrContainsSecret) {
		t.Fatalf("legacy service expected typed secret refusal, got %v", err)
	}
}
