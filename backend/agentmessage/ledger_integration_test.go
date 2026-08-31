package agentmessage_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/db"
)

func openEnvelopeSecurityDB(t *testing.T, agentNames ...string) (*agentmessage.Service, int64, map[string]int64) {
	t.Helper()
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
	project, err := db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Security','SEC')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	agents := make(map[string]int64, len(agentNames))
	for _, name := range agentNames {
		result, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, projectID, name)
		if err != nil {
			t.Fatal(err)
		}
		agents[name], _ = result.LastInsertId()
	}
	return agentmessage.NewService(db.DB), projectID, agents
}

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

func TestEnvelopeLedgerDoesNotWidenAgentScopeThroughForeignProductSession(t *testing.T) {
	svc, projectID, agents := openEnvelopeSecurityDB(t, "sender", "receiver")
	foreignProject, err := db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Foreign scope','FRN')`)
	if err != nil {
		t.Fatal(err)
	}
	foreignProjectID, _ := foreignProject.LastInsertId()
	user, err := db.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES('ledger-scope-user','x','admin','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	foreignSessionID := "17e5d8f7-0b11-4bee-a8a4-a11406de865a"
	if _, err := db.DB.Exec(`INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,title,created_by_user_id,updated_by_user_id)
		VALUES(?,?,'paimos','Foreign conversation',?,?)`, foreignSessionID, foreignProjectID, userID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO agent_message_allowlist(receiver_agent_id,sender_agent_id) VALUES(?,?)`, agents["receiver"], agents["sender"]); err != nil {
		t.Fatal(err)
	}
	envelope, err := svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:receiver", Body: "project-scoped message",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE agent_messages SET product_session_id=? WHERE message_id=?`, foreignSessionID, envelope.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetEnvelope(context.Background(), foreignProjectID, envelope.MessageID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign product session widened agent envelope scope: %v", err)
	}
	foreignRows, err := svc.ListEnvelopes(context.Background(), agentmessage.ListFilter{ProjectID: foreignProjectID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(foreignRows) != 0 {
		t.Fatalf("foreign project listed agent envelope through product session: %#v", foreignRows)
	}
	if own, err := svc.GetEnvelope(context.Background(), projectID, envelope.MessageID); err != nil || own.MessageID != envelope.MessageID {
		t.Fatalf("sender project lost its envelope: envelope=%#v err=%v", own, err)
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

func TestCanonicalEnvelopeHoldsAndSurfacesExplicitActionRequests(t *testing.T) {
	svc, projectID, agents := openEnvelopeSecurityDB(t, "sender", "receiver")
	issue, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status) VALUES(?,817,'ticket','Security contract','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()

	explicit, err := svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:receiver", IssueID: &issueID,
		Body: "Restart the service", ActionRequest: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Delivered || !explicit.IsActionRequest || explicit.HeldReason != "action request - requires human approval" {
		t.Fatalf("explicit action request escaped human gate: %#v", explicit)
	}
	held, err := svc.ListEnvelopes(context.Background(), agentmessage.ListFilter{ProjectID: projectID, IssueID: &issueID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0].MessageID != explicit.MessageID || !held[0].IsActionRequest {
		t.Fatalf("issue inspection did not surface held action request: %#v", held)
	}

	if err := svc.AllowSender(context.Background(), projectID, "codex:receiver", "paimos:sender"); err != nil {
		t.Fatal(err)
	}
	heuristic, err := svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:receiver", Body: "Please execute the maintenance command",
	})
	if err != nil {
		t.Fatal(err)
	}
	if heuristic.Delivered || !heuristic.IsActionRequest {
		t.Fatalf("heuristic fallback did not hold action request: %#v", heuristic)
	}
	neutral, err := svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:receiver", Body: "Maintenance window observed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !neutral.Delivered || neutral.IsActionRequest {
		t.Fatalf("neutral allowlisted message not delivered: %#v", neutral)
	}
	var deliveredActions int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_messages WHERE delivered=1 AND is_action_request=1`).Scan(&deliveredActions); err != nil || deliveredActions != 0 {
		t.Fatalf("delivered action rows=%d err=%v", deliveredActions, err)
	}
	_, err = svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:receiver", Body: "credential: abcdefghijk",
	})
	if !errors.Is(err, agentmessage.ErrContainsSecret) {
		t.Fatalf("canonical secret rejection error=%v", err)
	}
	var senderID int64
	if err := db.DB.QueryRow(`SELECT from_agent_id FROM agent_messages WHERE message_id=?`, neutral.MessageID).Scan(&senderID); err != nil || senderID != agents["sender"] {
		t.Fatalf("trusted sender id=%d err=%v", senderID, err)
	}
}

func TestCanonicalEnvelopeUnlistedSenderIsHeldThenAllowlisted(t *testing.T) {
	svc, projectID, _ := openEnvelopeSecurityDB(t, "sender", "receiver")
	held, err := svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:receiver", Body: "Observation only",
	})
	if err != nil {
		t.Fatal(err)
	}
	if held.Delivered || held.HeldReason != "sender not in receiver allowlist" {
		t.Fatalf("unlisted message=%#v", held)
	}
	page, err := svc.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: "codex:receiver", Agent: "receiver", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 0 {
		t.Fatalf("held message leaked into agent inbox: %#v", page.Messages)
	}
	if err := svc.AllowSender(context.Background(), projectID, "codex:receiver", "paimos:sender"); err != nil {
		t.Fatal(err)
	}
	delivered, err := svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:receiver", Body: "Second observation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !delivered.Delivered || delivered.HeldReason != "" {
		t.Fatalf("allowlisted message=%#v", delivered)
	}
}

func TestCanonicalEnvelopeLoopTerminatesAtHopCeiling(t *testing.T) {
	svc, projectID, _ := openEnvelopeSecurityDB(t, "alpha", "beta")
	if err := svc.AllowSender(context.Background(), projectID, "codex:beta", "paimos:alpha"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AllowSender(context.Background(), projectID, "codex:alpha", "paimos:beta"); err != nil {
		t.Fatal(err)
	}
	last, err := svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "alpha", To: "codex:beta", Body: "hop one",
	})
	if err != nil {
		t.Fatal(err)
	}
	for hop := 2; hop <= agentmessage.MaxHopCount; hop++ {
		sender, receiver := "beta", "alpha"
		if hop%2 == 1 {
			sender, receiver = "alpha", "beta"
		}
		last, err = svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
			ProjectID: projectID, Sender: sender, To: "codex:" + receiver,
			ReplyTo: last.MessageID, Body: "loop observation",
		})
		if err != nil {
			t.Fatalf("hop %d: %v", hop, err)
		}
		if last.Hop != hop {
			t.Fatalf("hop=%d want=%d", last.Hop, hop)
		}
	}
	_, err = svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "beta", To: "codex:alpha", ReplyTo: last.MessageID, Body: "must stop",
	})
	if !errors.Is(err, agentmessage.ErrHopLimitExceeded) {
		t.Fatalf("hop ceiling error=%v", err)
	}
}

func TestCanonicalEnvelopeRateAndInboxBatchBounds(t *testing.T) {
	svc, projectID, _ := openEnvelopeSecurityDB(t, "sender-a", "sender-b", "receiver")
	for _, sender := range []string{"sender-a", "sender-b"} {
		if err := svc.AllowSender(context.Background(), projectID, "codex:receiver", "paimos:"+sender); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < agentmessage.MaxMessagesPerMin; i++ {
		if _, err := svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
			ProjectID: projectID, Sender: "sender-a", To: "codex:receiver", Body: "bounded observation",
		}); err != nil {
			t.Fatalf("sender-a message %d: %v", i+1, err)
		}
	}
	_, err := svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender-a", To: "codex:receiver", Body: "eleventh observation",
	})
	if !errors.Is(err, agentmessage.ErrRateLimitExceeded) {
		t.Fatalf("rate bound error=%v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := svc.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
			ProjectID: projectID, Sender: "sender-b", To: "codex:receiver", Body: "second sender observation",
		}); err != nil {
			t.Fatalf("sender-b message %d: %v", i+1, err)
		}
	}
	first, err := svc.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: "codex:receiver", Agent: "receiver", Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Messages) != agentmessage.MaxDeliveredPerTurn {
		t.Fatalf("first inbox batch=%d want=%d", len(first.Messages), agentmessage.MaxDeliveredPerTurn)
	}
	second, err := svc.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: "codex:receiver", Agent: "receiver", AfterID: first.NextCursor, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Messages) != 6 {
		t.Fatalf("second inbox batch=%d want=6", len(second.Messages))
	}
	if strings.Contains(first.Messages[0].Parts[0].Text, "<paimos-message") {
		t.Fatal("storage/service layer should return structured raw body; framing belongs to the HTTP delivery boundary")
	}
}
