// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmessage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	paimosdb "github.com/inspr-at/paimos/backend/db"
)

func obligationState(t *testing.T, messageID string) (string, int64, sql.NullInt64) {
	t.Helper()
	var state string
	var count int64
	var closing sql.NullInt64
	if err := paimosdb.DB.QueryRow(`SELECT obligation.state,obligation.resurface_count,obligation.closing_message_row_id
		FROM agent_reply_obligations obligation JOIN agent_messages message ON message.id=obligation.message_row_id
		WHERE message.message_id=?`, messageID).Scan(&state, &count, &closing); err != nil {
		t.Fatal(err)
	}
	return state, count, closing
}

func TestReplyObligationIsExplicitIdempotentAndClosedOnlyByExactReply(t *testing.T) {
	service, projectID := openBusTestDB(t)
	allowBusSender(t, service, projectID, "codex:codex")
	if err := service.AllowSender(context.Background(), projectID, "paimos:sender", "codex:codex"); err != nil {
		t.Fatal(err)
	}
	if err := service.AllowSender(context.Background(), projectID, "paimos:sender", "codex:amy"); err != nil {
		t.Fatal(err)
	}

	plain, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "routine", IdempotencyKey: "plain-default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plain.ExpectsReply {
		t.Fatal("omitted expects_reply changed the existing send default")
	}
	var plainObligations int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_reply_obligations`).Scan(&plainObligations); err != nil || plainObligations != 0 {
		t.Fatalf("default obligations=%d err=%v", plainObligations, err)
	}

	original, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", SessionID: "sender-session", To: "codex:codex", Body: "please answer",
		ExpectsReply: true, IdempotencyKey: "reply-obligation-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !original.ExpectsReply || !original.Delivered {
		t.Fatalf("original=%#v", original)
	}
	replay, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", SessionID: "sender-session", To: "codex:codex", Body: "please answer",
		ExpectsReply: true, IdempotencyKey: "reply-obligation-create",
	})
	if err != nil || replay.MessageID != original.MessageID {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	if _, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "please answer", IdempotencyKey: "reply-obligation-create",
	}); err == nil {
		t.Fatal("reusing the key without expects_reply did not conflict")
	} else {
		var codedErr *CodedError
		if !errors.As(err, &codedErr) || codedErr.Code != "agent_message_idempotency_conflict" {
			t.Fatalf("idempotency error=%v", err)
		}
	}
	var obligations, opened int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_reply_obligations`).Scan(&obligations); err != nil {
		t.Fatal(err)
	}
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_reply_obligation_events WHERE event_kind='opened'`).Scan(&opened); err != nil {
		t.Fatal(err)
	}
	if obligations != 1 || opened != 1 {
		t.Fatalf("obligations/opened=%d/%d", obligations, opened)
	}

	if _, err := service.AckInbox(context.Background(), AckInput{
		ProjectID: projectID, Address: "codex:codex", Agent: "codex", Cursor: original.Cursor,
	}); err != nil {
		t.Fatal(err)
	}
	if state, _, closing := obligationState(t, original.MessageID); state != "open" || closing.Valid {
		t.Fatalf("inbox acknowledgement closed obligation: %s %+v", state, closing)
	}

	wrong, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "amy", To: "paimos:sender", ReplyTo: original.MessageID,
		Body: "third-party reply", IdempotencyKey: "wrong-party-reply",
	})
	if err != nil {
		t.Fatal(err)
	}
	if wrong.ReplyTo != original.MessageID {
		t.Fatalf("wrong reply=%#v", wrong)
	}
	if state, _, _ := obligationState(t, original.MessageID); state != "open" {
		t.Fatalf("unaddressed third-party reply closed obligation: %s", state)
	}

	replyInput := SendEnvelopeInput{
		ProjectID: projectID, Sender: "codex", To: "paimos:sender", ReplyTo: original.MessageID,
		Body: "exact answer", IdempotencyKey: "exact-reply",
	}
	reply, err := service.SendEnvelope(context.Background(), replyInput)
	if err != nil {
		t.Fatal(err)
	}
	replyReplay, err := service.SendEnvelope(context.Background(), replyInput)
	if err != nil || replyReplay.MessageID != reply.MessageID {
		t.Fatalf("reply replay=%#v err=%v", replyReplay, err)
	}
	state, _, closing := obligationState(t, original.MessageID)
	if state != "closed" || !closing.Valid || closing.Int64 != reply.Cursor {
		t.Fatalf("state/count/closing=%s/%+v reply=%d", state, closing, reply.Cursor)
	}
	var closedEvents int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_reply_obligation_events WHERE event_kind='closed'
		AND related_message_row_id=?`, reply.Cursor).Scan(&closedEvents); err != nil || closedEvents != 1 {
		t.Fatalf("closed events=%d err=%v", closedEvents, err)
	}
}

func TestReplyObligationResurfacesWithBoundedBackoffAndStopsOnReply(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:codex", Adapter: AdapterCodex, TargetKind: TargetKindCodexThread,
		TargetRef: "01a069e0-f6b5-70d1-a487-02d1d00f1019", MaximumLevel: "simple", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "paimos:sender", Adapter: AdapterCodex, TargetKind: TargetKindCodexThread,
		TargetRef: "01a069e0-f6b5-70d1-a487-02d1d00f1020", MaximumLevel: "simple", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	allowBusSender(t, service, projectID, "codex:codex")
	if err := service.AllowSender(context.Background(), projectID, "paimos:sender", "codex:codex"); err != nil {
		t.Fatal(err)
	}
	original, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "answer requested", ExpectsReply: true,
		IdempotencyKey: "resurface-original",
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := service.ListInbox(context.Background(), InboxInput{
		ProjectID: projectID, Address: "codex:codex", Agent: "codex", WorkerAdapter: AdapterCodex, Limit: 10,
	})
	if err != nil || len(inbox.Messages) != 1 || inbox.Messages[0].DeliveryWork == nil {
		t.Fatalf("reply delivery lease=%#v err=%v", inbox, err)
	}
	if _, err := service.CompleteLocalDelivery(context.Background(), CompleteDeliveryInput{
		ProjectID: projectID, Address: "codex:codex", Agent: "codex", Cursor: original.Cursor,
		DeliveryID: inbox.Messages[0].DeliveryWork.DeliveryID, EffectiveLevel: "simple",
	}); err != nil {
		t.Fatal(err)
	}
	if state, _, closing := obligationState(t, original.MessageID); state != "open" || closing.Valid {
		t.Fatalf("confirmed delivery closed reply obligation: %s %+v", state, closing)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE agent_reply_obligations SET next_attention_at='2000-01-01T00:00:00.000Z'
		WHERE message_row_id=?`, original.Cursor); err != nil {
		t.Fatal(err)
	}
	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 1 {
		t.Fatalf("first projection=%d err=%v", inserted, err)
	}
	state, count, _ := obligationState(t, original.MessageID)
	if state != "open" || count != 1 {
		t.Fatalf("first state/count=%s/%d", state, count)
	}
	var kind, reason string
	var sequence int64
	if err := paimosdb.DB.QueryRow(`SELECT attention_kind,reason_code,source_sequence FROM agent_attention_items
		WHERE source_kind='reply_obligation' AND source_id=?`, original.MessageID).Scan(&kind, &reason, &sequence); err != nil {
		t.Fatal(err)
	}
	if kind != "reply_overdue" || reason != "reply_expected" || sequence != 1 {
		t.Fatalf("attention=%q/%q/%d", kind, reason, sequence)
	}
	var deliveryInvitations int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_attention_items
		WHERE source_kind='agent_message_delivery' AND source_id=?`, inbox.Messages[0].DeliveryWork.DeliveryID).Scan(&deliveryInvitations); err != nil {
		t.Fatal(err)
	}
	if deliveryInvitations != 0 {
		t.Fatalf("confirmed delivery produced %d resend invitations", deliveryInvitations)
	}
	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 0 {
		t.Fatalf("backoff did not suppress immediate replay: inserted=%d err=%v", inserted, err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE agent_reply_obligations SET next_attention_at='2000-01-01T00:00:00.000Z'
		WHERE message_row_id=?`, original.Cursor); err != nil {
		t.Fatal(err)
	}
	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 1 {
		t.Fatalf("second projection=%d err=%v", inserted, err)
	}
	var nextText string
	if err := paimosdb.DB.QueryRow(`SELECT next_attention_at FROM agent_reply_obligations WHERE message_row_id=?`, original.Cursor).Scan(&nextText); err != nil {
		t.Fatal(err)
	}
	next, err := time.Parse("2006-01-02T15:04:05.000Z", nextText)
	if err != nil {
		t.Fatal(err)
	}
	delay := time.Until(next)
	if delay < 59*time.Minute || delay > 61*time.Minute {
		t.Fatalf("second backoff=%s want about 1h", delay)
	}

	if _, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "codex", To: "paimos:sender", ReplyTo: original.MessageID,
		Body: "answer", IdempotencyKey: "resurface-close",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE agent_reply_obligations SET next_attention_at='2000-01-01T00:00:00.000Z'
		WHERE message_row_id=?`, original.Cursor); err == nil {
		t.Fatal("closed obligation accepted a new attention schedule")
	}
	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 0 {
		t.Fatalf("closed obligation resurfaced: inserted=%d err=%v", inserted, err)
	}
	var events, items int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_reply_obligation_events WHERE message_row_id=?`, original.Cursor).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_attention_items WHERE source_kind='reply_obligation' AND source_id=?`, original.MessageID).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if events != 4 || items != 2 {
		t.Fatalf("events/items=%d/%d want opened+2 resurfaced+closed / 2", events, items)
	}
}

func TestClosedReplyAndResolvedActionDisappearFromActionableAttention(t *testing.T) {
	service, projectID := openBusTestDB(t)
	actorID := configureAttentionReceiver(t, service, projectID)
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:codex", Adapter: AdapterCodex, TargetKind: TargetKindCodexThread,
		TargetRef: "01a069e0-f6b5-70d1-a487-02d1d00f1019", MaximumLevel: "simple", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "paimos:sender", Adapter: AdapterCodex, TargetKind: TargetKindCodexThread,
		TargetRef: "01a069e0-f6b5-70d1-a487-02d1d00f1020", MaximumLevel: "simple", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	allowBusSender(t, service, projectID, "codex:codex")
	if err := service.AllowSender(context.Background(), projectID, "paimos:sender", "codex:codex"); err != nil {
		t.Fatal(err)
	}
	original, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "answer requested",
		ExpectsReply: true, IdempotencyKey: "active-feed-reply",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE agent_reply_obligations SET next_attention_at='2000-01-01T00:00:00.000Z'
		WHERE message_row_id=?`, original.Cursor); err != nil {
		t.Fatal(err)
	}
	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 1 {
		t.Fatalf("reply attention projection=%d err=%v", inserted, err)
	}
	replyPage, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil || replyPage.Work == nil || len(replyPage.Items) != 1 {
		t.Fatalf("reply attention=%#v err=%v", replyPage, err)
	}
	if _, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "codex", To: "paimos:sender", ReplyTo: original.MessageID,
		Body: "answer", IdempotencyKey: "active-feed-close",
	}); err != nil {
		t.Fatal(err)
	}
	closedPage, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil || len(closedPage.Items) != 0 || closedPage.Work != nil {
		t.Fatalf("closed reply remained actionable: %#v err=%v", closedPage, err)
	}
	var replyBatchState string
	if err := paimosdb.DB.QueryRow(`SELECT state FROM agent_attention_batches WHERE batch_id=?`, replyPage.Work.BatchID).Scan(&replyBatchState); err != nil {
		t.Fatal(err)
	}
	if replyBatchState != "superseded" {
		t.Fatalf("closed reply batch state=%q", replyBatchState)
	}

	held, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "human decision requested",
		ActionRequest: true, IdempotencyKey: "active-feed-held",
	})
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 1 {
		t.Fatalf("held attention projection=%d err=%v", inserted, err)
	}
	heldPage, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil || heldPage.Work == nil || len(heldPage.Items) != 1 || heldPage.Items[0].SourceID != held.MessageID {
		t.Fatalf("held attention=%#v err=%v", heldPage, err)
	}
	authority := func(context.Context, *sql.Tx) (int64, string, error) { return actorID, "session:test", nil }
	if _, err := service.ResolveHeldMessage(context.Background(), ResolveHeldMessageInput{
		ProjectID: projectID, MessageID: held.MessageID, Outcome: "resolved",
		IdempotencyKey: "active-feed-resolve", Authority: authority,
	}); err != nil {
		t.Fatal(err)
	}
	resolvedPage, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil || len(resolvedPage.Items) != 0 || resolvedPage.Work != nil {
		t.Fatalf("resolved action remained actionable: %#v err=%v", resolvedPage, err)
	}
	var heldBatchState string
	if err := paimosdb.DB.QueryRow(`SELECT state FROM agent_attention_batches WHERE batch_id=?`, heldPage.Work.BatchID).Scan(&heldBatchState); err != nil {
		t.Fatal(err)
	}
	if heldBatchState != "superseded" {
		t.Fatalf("resolved action batch state=%q", heldBatchState)
	}
}

func TestHumanResolutionIsImmutableValueFreeAndIdempotent(t *testing.T) {
	service, projectID := openBusTestDB(t)
	actor, err := paimosdb.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES('resolver','disabled','admin','active')`)
	if err != nil {
		t.Fatal(err)
	}
	actorID, _ := actor.LastInsertId()
	authority := func(context.Context, *sql.Tx) (int64, string, error) {
		return actorID, "session:019-session-audit", nil
	}
	held, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:codex", Body: "Restart the worker",
		ActionRequest: true, IdempotencyKey: "held-action",
	})
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err := paimosdb.DB.QueryRow(`SELECT printf('%d|%d|%s|%s',is_action_request,delivered,held_reason,body)
		FROM agent_messages WHERE message_id=?`, held.MessageID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	input := ResolveHeldMessageInput{ProjectID: projectID, MessageID: held.MessageID, Outcome: "dismissed",
		IdempotencyKey: "resolution-retry-key", Authority: authority}
	first, err := service.ResolveHeldMessage(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.ResolveHeldMessage(context.Background(), input)
	if err != nil || replay.ResolutionID != first.ResolutionID {
		t.Fatalf("resolution replay=%#v err=%v", replay, err)
	}
	conflict := input
	conflict.Outcome = "resolved"
	if _, err := service.ResolveHeldMessage(context.Background(), conflict); err == nil {
		t.Fatal("different outcome reused idempotency key")
	} else {
		var codedErr *CodedError
		if !errors.As(err, &codedErr) || codedErr.Code != "agent_message_resolution_idempotency_conflict" {
			t.Fatalf("different outcome error=%v", err)
		}
	}
	secondKey := input
	secondKey.IdempotencyKey = "second-key"
	if _, err := service.ResolveHeldMessage(context.Background(), secondKey); err == nil {
		t.Fatal("held message accepted a second resolution fact")
	}
	var after string
	if err := paimosdb.DB.QueryRow(`SELECT printf('%d|%d|%s|%s',is_action_request,delivered,held_reason,body)
		FROM agent_messages WHERE message_id=?`, held.MessageID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("held row mutated before=%q after=%q", before, after)
	}
	if first.ActorUserID != actorID || first.ActorSessionID != "session:019-session-audit" || first.Outcome != "dismissed" {
		t.Fatalf("resolution=%#v", first)
	}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Restart the worker", "resolution-retry-key"} {
		if string(raw) == forbidden || containsBytes(raw, []byte(forbidden)) {
			t.Fatalf("resolution leaked value %q: %s", forbidden, raw)
		}
	}
	var resolutions int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_human_resolutions`).Scan(&resolutions); err != nil || resolutions != 1 {
		t.Fatalf("resolutions=%d err=%v", resolutions, err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE agent_message_human_resolutions SET outcome='resolved' WHERE resolution_id=?`, first.ResolutionID); err == nil {
		t.Fatal("immutable human resolution was rewritten")
	}
}

func containsBytes(haystack, needle []byte) bool {
	for len(needle) <= len(haystack) {
		if string(haystack[:len(needle)]) == string(needle) {
			return true
		}
		haystack = haystack[1:]
	}
	return false
}
