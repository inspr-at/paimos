// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmessage

import (
	"context"
	"testing"
)

func TestReadAckDoesNotStrandAuthorizedFIFOAfterTargetRequeue(t *testing.T) {
	s, project := openBusTestDB(t)
	addBusClaudeReceiver(t, s, project)
	ctx := context.Background()
	send := func(body string) *Envelope {
		t.Helper()
		m, err := s.SendEnvelope(ctx, SendEnvelopeInput{ProjectID: project, Sender: "sender", To: "claude:claude", Body: body, DeliveryLevel: "simple"})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	first := send("first fixture observation")
	second := send("second fixture observation")
	if _, err := s.AckInbox(ctx, AckInput{ProjectID: project, Address: "claude:claude", Agent: "claude", Cursor: second.Cursor}); err != nil {
		t.Fatal(err)
	}
	statuses, err := s.ListDeliveryStatus(ctx, project)
	if err != nil || len(statuses) != 2 || statuses[0].State != "blocked" {
		t.Fatal("read acknowledgement changed durable delivery authorization")
	}
	target, err := s.RegisterTarget(ctx, RegisterTargetInput{ProjectID: project, Address: "claude:claude", Adapter: AdapterClaudeChannel, TargetKind: TargetKindClaudeSession, TargetRef: busTestClaudeLocalSession})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequeueMissingTargets(ctx, project, "claude:claude"); err != nil {
		t.Fatal(err)
	}
	third := send("third fixture observation")
	for _, want := range []*Envelope{first, second, third} {
		// A long-lived listener may already have the higher read cursor in memory.
		page, err := s.ListInbox(ctx, InboxInput{ProjectID: project, Address: "claude:claude", Agent: "claude", WorkerAdapter: AdapterClaudeChannel, TargetID: target.ID, AfterID: third.Cursor})
		if err != nil {
			t.Fatal(err)
		}
		var got *Envelope
		for i := range page.Messages {
			m := &page.Messages[i]
			if m.DeliveryWork != nil && m.DeliveryWork.State == "leased" {
				if got != nil {
					t.Fatal("multiple FIFO rows leased")
				}
				got = m
			}
		}
		if want == first {
			blocked := 0
			for _, message := range page.Messages {
				if message.DeliveryWork != nil && message.DeliveryWork.FallbackReason == "fifo_blocked" {
					blocked++
					if message.DeliveryWork.TargetRef != "" {
						t.Fatal("FIFO diagnostic disclosed target")
					}
				}
			}
			if blocked != 2 {
				t.Fatal("FIFO wait was silently omitted")
			}
			statuses, e := s.ListDeliveryStatus(ctx, project)
			if e != nil || statuses[1].LastErrorCode != "fifo_blocked" {
				t.Fatal("FIFO diagnostic not durable")
			}
		}
		if got == nil || got.MessageID != want.MessageID {
			t.Fatal("authorized delivery below read cursor was stranded or FIFO changed")
		}
		completion := CompleteDeliveryInput{ProjectID: project, Address: "claude:claude", Agent: "claude", Cursor: got.Cursor, DeliveryID: got.DeliveryWork.DeliveryID, TargetID: target.ID, EffectiveLevel: "simple"}
		if _, err := s.CompleteLocalDelivery(ctx, completion); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CompleteLocalDelivery(ctx, completion); err != nil {
			t.Fatal("completion replay lost idempotency")
		}
	}
	page, err := s.ListInbox(ctx, InboxInput{ProjectID: project, Address: "claude:claude", Agent: "claude", WorkerAdapter: AdapterClaudeChannel})
	if err != nil || len(page.Messages) != 0 {
		t.Fatal("completed work was offered twice")
	}
}

func TestDeliveryFIFODoesNotCrossProjectForSameAddress(t *testing.T) {
	s, project := openBusTestDB(t)
	ctx := context.Background()
	addBusClaudeReceiver(t, s, project)
	if _, err := s.SendEnvelope(ctx, SendEnvelopeInput{ProjectID: project, Sender: "sender", To: "claude:claude", Body: "older fixture observation", DeliveryLevel: "simple"}); err != nil {
		t.Fatal(err)
	}
	result, err := s.db.Exec(`INSERT INTO projects(name,key) VALUES('Other fixture','OTH')`)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := result.LastInsertId()
	if _, err := s.db.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'sender'),(?,'claude')`, other, other); err != nil {
		t.Fatal(err)
	}
	allowBusSender(t, s, other, "claude:claude")
	if _, err := s.RegisterTarget(ctx, RegisterTargetInput{ProjectID: other, Address: "claude:claude", Adapter: AdapterClaudeChannel, TargetKind: TargetKindClaudeSession, TargetRef: busTestClaudeLocalSession}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendEnvelope(ctx, SendEnvelopeInput{ProjectID: other, Sender: "sender", To: "claude:claude", Body: "newer fixture observation", DeliveryLevel: "simple"}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListInbox(ctx, InboxInput{ProjectID: other, Address: "claude:claude", Agent: "claude", WorkerAdapter: AdapterClaudeChannel})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].DeliveryWork.State != "leased" {
		t.Fatal("another project's FIFO blocked delivery")
	}
}
