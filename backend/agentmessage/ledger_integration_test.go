package agentmessage_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/db"
)

func TestEnvelopeLedgerResolvesAddressesAndSupportsCursorReads(t *testing.T) {
	oldDir, oldMode := os.Getenv("DATA_DIR"), os.Getenv("PAIMOS_TEST_MODE")
	t.Cleanup(func() {
		if db.DB != nil {
			_ = db.DB.Close()
			db.DB = nil
		}
		_ = os.Setenv("DATA_DIR", oldDir)
		_ = os.Setenv("PAIMOS_TEST_MODE", oldMode)
	})
	_ = os.Setenv("DATA_DIR", t.TempDir())
	_ = os.Setenv("PAIMOS_TEST_MODE", "1")
	if err := db.Open(); err != nil {
		t.Fatal(err)
	}
	project, err := db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Ledger','LED')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	sender, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'builder')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'reviewer')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	senderID, _ := sender.LastInsertId()
	receiverID, _ := receiver.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO agent_message_allowlist(receiver_agent_id,sender_agent_id) VALUES(?,?)`, receiverID, senderID); err != nil {
		t.Fatal(err)
	}

	svc := agentmessage.NewService(db.DB)
	_, err = svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{ProjectID: projectID, Sender: "builder", To: "codex:missing", Body: "hello"})
	var coded *agentmessage.CodedError
	if !errors.As(err, &coded) || coded.Code != "agent_message_addressee_unknown" {
		t.Fatalf("unknown addressee error=%v", err)
	}

	first, err := svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{ProjectID: projectID, Sender: "builder", SessionID: "session-1", To: "codex:reviewer", Body: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Delivered || first.From != "paimos:builder" || first.To != "codex:reviewer" || first.Hop != 1 || first.Cursor < 1 {
		t.Fatalf("unexpected first envelope: %#v", first)
	}
	second, err := svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{ProjectID: projectID, Sender: "builder", To: "codex:reviewer", ReplyTo: first.MessageID, Body: "follow-up"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ThreadID != first.ThreadID || second.Hop != 2 {
		t.Fatalf("reply chain lost: first=%#v second=%#v", first, second)
	}
	rows, err := svc.ListEnvelopes(context.Background(), agentmessage.ListFilter{ProjectID: projectID, To: "codex:reviewer", ThreadID: first.ThreadID, AfterID: first.Cursor, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].MessageID != second.MessageID {
		t.Fatalf("cursor/thread read=%#v", rows)
	}
	if _, err := svc.ListInbox(context.Background(), agentmessage.InboxInput{ProjectID: projectID, Address: "codex:reviewer", Agent: "builder", Limit: 10}); err == nil {
		t.Fatal("mismatched attributed agent should not read another inbox")
	} else if !errors.As(err, &coded) || coded.Code != "agent_message_addressee_mismatch" {
		t.Fatalf("mismatched inbox error=%v", err)
	}
	page, err := svc.ListInbox(context.Background(), agentmessage.InboxInput{ProjectID: projectID, Address: "codex:reviewer", Agent: "reviewer", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Cursor != 0 || page.NextCursor != second.Cursor || len(page.Messages) != 2 {
		t.Fatalf("initial inbox page=%#v", page)
	}
	state, err := svc.AckInbox(context.Background(), agentmessage.AckInput{ProjectID: projectID, Address: "codex:reviewer", Agent: "reviewer", Cursor: second.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if state.Cursor != second.Cursor {
		t.Fatalf("ack state=%#v", state)
	}
	page, err = svc.ListInbox(context.Background(), agentmessage.InboxInput{ProjectID: projectID, Address: "codex:reviewer", Agent: "reviewer", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Cursor != second.Cursor || page.NextCursor != second.Cursor || len(page.Messages) != 0 {
		t.Fatalf("acked inbox page=%#v", page)
	}
	var readRows int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_messages WHERE to_address='codex:reviewer' AND read_at IS NOT NULL`).Scan(&readRows); err != nil || readRows != 2 {
		t.Fatalf("read rows=%d err=%v", readRows, err)
	}
	if _, err := svc.AckInbox(context.Background(), agentmessage.AckInput{ProjectID: projectID, Address: "codex:reviewer", Agent: "reviewer", Cursor: second.Cursor + 100}); err == nil {
		t.Fatal("ack must reject a cursor outside the delivered inbox")
	} else if !errors.As(err, &coded) || coded.Code != "agent_message_cursor_unknown" {
		t.Fatalf("unknown cursor error=%v", err)
	}
}

func TestEnvelopeLedgerAllowSenderIsNameScopedAndIdempotent(t *testing.T) {
	oldDir, oldMode := os.Getenv("DATA_DIR"), os.Getenv("PAIMOS_TEST_MODE")
	t.Cleanup(func() {
		if db.DB != nil {
			_ = db.DB.Close()
			db.DB = nil
		}
		_ = os.Setenv("DATA_DIR", oldDir)
		_ = os.Setenv("PAIMOS_TEST_MODE", oldMode)
	})
	_ = os.Setenv("DATA_DIR", t.TempDir())
	_ = os.Setenv("PAIMOS_TEST_MODE", "1")
	if err := db.Open(); err != nil {
		t.Fatal(err)
	}
	project, _ := db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Allow','ALW')`)
	projectID, _ := project.LastInsertId()
	_, _ = db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'sender'),(?,'receiver')`, projectID, projectID)
	svc := agentmessage.NewService(db.DB)
	for range 2 {
		if err := svc.AllowSender(context.Background(), projectID, "codex:receiver", "claude:sender"); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_allowlist`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("allowlist count=%d err=%v", count, err)
	}
}

func TestEnvelopeLedgerEnforcesBodyCap(t *testing.T) {
	svc := agentmessage.NewService(nil)
	_, err := svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{Sender: "sender", To: "codex:receiver", Body: string(make([]byte, agentmessage.MaxBodySize+1))})
	if !errors.Is(err, agentmessage.ErrBodyTooLarge) {
		t.Fatalf("oversize error=%v", err)
	}
}
