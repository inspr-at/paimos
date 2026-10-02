// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"testing"
	"time"
)

// Exercise P2 matching against real P3 delivery and P4 publication, rather than
// manufacturing an active Decision or marking effects delivered in a fixture.
func TestDeskChainPublishedAlwaysReuseDeliveryAndCorrection(t *testing.T) {
	f := deliveryFixtureFor(t)
	in := input()
	in.SessionID = f.session
	q := f.ask(t, in)
	q = question(t, request(t.Context(), f.mux, f.person, "POST", "/api/questions/"+q.ID+"/decision", DecisionInput{RequestID: uid(), ExpectedRevision: q.Revision, OptionID: "a", Outcome: "always"}), 200)
	f.advance(10 * time.Second)
	f.dispatch(t)
	if outcomeRow(t, f.status(t, q.ID)).State != "delivered" || f.knowledge(t, q.Answer.ID).Status != "active" {
		t.Fatal("real Always publication did not finish")
	}
	f.expectEffects(t, q, "delivered")

	member := input()
	member.SessionID = f.otherSession
	member.Options[0], member.Options[1] = member.Options[1], member.Options[0]
	reused := question(t, request(t.Context(), f.mux, f.otherAgent, "POST", "/api/projects/"+f.project+"/questions", member), 201)
	if reused.ID != q.ID || reused.Answer.ID != q.Answer.ID || len(reused.Askers) != 1 || len(reused.Pending) != 3 || f.reuseCount(t, q) != 1 {
		t.Fatal("matching did not reuse the real published Always")
	}
	asker := reused.Askers[0]
	if asker.FromRecord == nil || asker.FromRecord.DecisionID != q.Answer.ID || asker.FromRecord.Revision != 2 || asker.RequestID != member.RequestID || asker.SessionID != f.otherSession || asker.ReplyRootID == q.Askers[0].ReplyRootID {
		t.Fatal("reused membership lost immutable provenance or its own route")
	}
	effects := map[string]string{}
	for _, e := range reused.Pending {
		if e.Kind == "outcome" {
			if e.ID != outcomeRow(t, f.status(t, q.ID)).ID || e.State != "delivered" || e.AskerID != "" {
				t.Fatal("reuse recreated the shared published outcome")
			}
			continue
		}
		if e.AskerID != asker.ID || e.State != "pending" || e.DeliverAfter.After(time.Unix(0, f.now.Load())) || effects[e.Kind] != "" {
			t.Fatal("reuse added another grace or lost an effect identity")
		}
		effects[e.Kind] = e.ID
	}
	if len(effects) != 2 || effects["inbox"] == "" || effects["comment"] == "" {
		t.Fatal("reuse omitted its per-asker inbox or comment")
	}
	// No clock advancement: both immediate reuse effects must dispatch now.
	f.dispatch(t)
	status := question(t, request(t.Context(), f.mux, f.otherAgent, "GET", "/api/questions/"+q.ID+"/status", nil), 200)
	for _, e := range status.Pending {
		if e.Kind == "outcome" {
			continue
		}
		if e.ID != effects[e.Kind] || e.State != "delivered" {
			t.Fatal("immediate reuse delivery changed identities or remained pending")
		}
		if e.Kind == "inbox" && (e.ReceiptState != "queued" || e.DeliverySessionID != f.otherSession) {
			t.Fatal("reuse delivery did not retain its exact session and honest receipt")
		}
	}
	if f.count(t, `SELECT count(*) FROM inbox_compat_messages WHERE recipient_session_id=$1 AND body::jsonb->>'answer_id'=$2 AND body::jsonb->>'asker_id'=$3 AND reply_to_id=$4`, f.otherSession, q.Answer.ID, asker.ID, asker.ReplyRootID) != 1 {
		t.Fatal("reuse did not deliver the correlated answer to the matching asker")
	}
	if f.count(t, `SELECT count(*) FROM events WHERE type='comment.created' AND node_id=$1 AND after->>'answer_id'=$2`, q.ID, q.Answer.ID) != 2 {
		t.Fatal("matching fan-out lost a ticketless comment")
	}
	replay := question(t, request(t.Context(), f.mux, f.otherAgent, "POST", "/api/projects/"+f.project+"/questions", member), 200)
	if replay.Askers[0].ID != asker.ID || f.reuseCount(t, q) != 1 {
		t.Fatal("retry changed membership or counted the same reuse twice")
	}
	f.dispatch(t)
	if f.count(t, `SELECT count(*) FROM inbox_messages`) != 2 || f.count(t, `SELECT count(*) FROM events WHERE type='knowledge.created' AND node_id=$1`, q.Answer.ID) != 1 {
		t.Fatal("reuse retry duplicated delivery or Always publication")
	}

	corrected := question(t, request(t.Context(), f.mux, f.person, "POST", "/api/questions/"+q.ID+"/decision", DecisionInput{RequestID: uid(), ExpectedRevision: q.Revision, OptionID: "b", Outcome: "always"}), 200)
	if corrected.Revision != 3 || corrected.Answer.Replaces != q.Answer.ID || len(corrected.Pending) != 5 {
		t.Fatal("real correction lost lineage or full matching fan-out")
	}
	if f.knowledge(t, q.Answer.ID).Status != "archived" || f.count(t, `SELECT count(*) FROM desk_decisions WHERE question_id=$1 AND state='active'`, q.ID) != 0 {
		t.Fatal("superseded Always remained reusable during correction grace")
	}
	f.advance(9 * time.Second)
	f.dispatch(t)
	if f.count(t, `SELECT count(*) FROM inbox_messages`) != 2 || outcomeRow(t, f.status(t, q.ID)).State != "pending" {
		t.Fatal("correction escaped the ten-second grace")
	}
	f.advance(time.Second)
	f.dispatch(t)
	f.expectEffects(t, corrected, "delivered")
	for _, session := range []string{f.session, f.otherSession} {
		if f.count(t, `SELECT count(*) FROM inbox_messages WHERE recipient_session_id=$1 AND body::jsonb->>'type'='question.correction' AND body::jsonb->>'answer_id'=$2 AND body::jsonb->>'replaces'=$3`, session, corrected.Answer.ID, q.Answer.ID) != 1 {
			t.Fatal("correction did not reach each exact matching session")
		}
	}
	current := question(t, request(t.Context(), f.mux, f.otherAgent, "GET", "/api/questions/"+q.ID, nil), 200)
	if current.Askers[0].FromRecord == nil || *current.Askers[0].FromRecord != *asker.FromRecord || current.Answer.ID != corrected.Answer.ID || f.reuseCount(t, q) != 1 {
		t.Fatal("real correction changed original reuse provenance")
	}
	if f.knowledge(t, corrected.Answer.ID).Status != "active" || f.knowledge(t, q.Answer.ID).Metadata["superseded_by"] != corrected.Answer.ID {
		t.Fatal("corrected Always lost its active projection or predecessor")
	}
	member.RequestID = uid()
	next := question(t, request(t.Context(), f.mux, f.otherAgent, "POST", "/api/projects/"+f.project+"/questions", member), 201)
	latest := next.Askers[len(next.Askers)-1]
	if next.ID != q.ID || next.Answer.ID != corrected.Answer.ID || latest.FromRecord == nil || latest.FromRecord.DecisionID != corrected.Answer.ID || latest.FromRecord.Revision != 3 {
		t.Fatal("matching reused the superseded source after correction publication")
	}
	if f.count(t, `SELECT reuse_count FROM desk_decisions WHERE question_id=$1 AND revision=3`, q.ID) != 1 || f.reuseCount(t, q) != 1 {
		t.Fatal("new reuse incremented the wrong immutable decision")
	}
}
