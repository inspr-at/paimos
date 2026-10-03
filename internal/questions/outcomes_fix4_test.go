// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFix4ReviewIdentityIncludesReference(t *testing.T) {
	first := EffectReview{Kind: "criterion", Ref: "ticket-one", Why: "First ticket needs review"}
	second := EffectReview{Kind: "criterion", Ref: "ticket-two", Why: "Second ticket needs review"}
	doctrine := EffectReview{Kind: "doctrine", Ref: second.Ref, Why: "A different kind needs review"}
	data := EffectData{}
	for _, review := range []EffectReview{first, second, doctrine} {
		data.requireReview(review)
	}
	if want := []EffectReview{first, second, doctrine}; !slices.Equal(data.ReviewRequired, want) {
		t.Fatalf("distinct review provenance was overwritten: got %+v, want %+v", data.ReviewRequired, want)
	}
	// Refresh only the matching kind and reference, without duplicating it.
	second.Why = "Updated reason for the second ticket"
	data.requireReview(second)
	if want := []EffectReview{first, second, doctrine}; !slices.Equal(data.ReviewRequired, want) {
		t.Fatalf("review refresh changed another reference: got %+v, want %+v", data.ReviewRequired, want)
	}
}

func TestFix4CorrectionKeepsSupersededReviewProvenance(t *testing.T) {
	f := deliveryFixtureFor(t)
	in := input()
	in.TicketID = f.ticket
	q := f.outcome(t, f.ask(t, in), "requirement", "Original criterion")
	f.advance(10 * time.Second)
	f.dispatch(t)
	if initial := outcomeRow(t, f.status(t, q.ID)); initial.State != "delivered" || initial.EffectData.Criterion == "" {
		t.Fatalf("missing original criterion: %+v", initial)
	}
	// A person's edit prevents removal of the first criterion. The next
	// Requirement carries its review alongside a new tracked criterion.
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE nodes SET fields=jsonb_set(fields,'{acceptance_criteria}',to_jsonb(replace(fields->>'acceptance_criteria','Original criterion','Person edited criterion'))),updated_at=clock_timestamp() WHERE id=$1`, f.ticket); err != nil {
		t.Fatal(err)
	}
	q = f.outcome(t, q, "requirement", "Replacement criterion")
	f.advance(10 * time.Second)
	f.dispatch(t)
	previous := outcomeRow(t, f.status(t, q.ID))
	if previous.State != "delivered" || previous.EffectData.Criterion == "" || len(previous.EffectData.ReviewRequired) != 1 {
		t.Fatalf("missing replacement and carried review: %+v", previous)
	}
	priorReview := previous.EffectData.ReviewRequired[0]
	if priorReview.Kind != "criterion" || priorReview.Ref != f.ticket || !strings.Contains(priorReview.Why, "edited or removed") {
		t.Fatalf("fixture did not record the person's edit: %+v", priorReview)
	}
	criteria := f.criteria(t)
	if !strings.Contains(criteria, "Person edited criterion") || !strings.Contains(criteria, previous.EffectData.Criterion) {
		t.Fatal("fixture lost the person's edit or replacement criterion")
	}
	// Losing access to that same ticket refreshes the current review reason.
	// The superseded effect must keep the reason recorded at its own revision.
	actor := decideOnlyPerson(t, f)
	q = question(t, request(t.Context(), f.mux, actor, "POST", "/api/questions/"+q.ID+"/decision", DecisionInput{RequestID: uid(), ExpectedRevision: q.Revision, Outcome: "once", Answer: "Only this question now"}), 200)
	f.advance(10 * time.Second)
	f.dispatch(t)
	current := outcomeRow(t, f.status(t, q.ID))
	if current.State != "delivered" || len(current.EffectData.ReviewRequired) != 1 {
		t.Fatalf("correction failed or duplicated the review: %+v", current)
	}
	updatedReview := current.EffectData.ReviewRequired[0]
	if updatedReview.Kind != priorReview.Kind || updatedReview.Ref != priorReview.Ref || updatedReview.Why == priorReview.Why || !strings.Contains(updatedReview.Why, "no longer editable") {
		t.Fatalf("current review was not refreshed: %+v", updatedReview)
	}
	var old EffectData
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT effect_data FROM desk_pending WHERE id=$1`, previous.ID).Scan(&old); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(old.ReviewRequired, previous.EffectData.ReviewRequired) {
		t.Fatalf("superseded review provenance was mutated: got %+v, want %+v", old.ReviewRequired, previous.EffectData.ReviewRequired)
	}
	if old.TicketID != f.ticket || old.Criterion != previous.EffectData.Criterion || old.SupersededBy != current.ID || current.EffectData.Supersedes != previous.ID {
		t.Fatal("correction lost criterion or supersession provenance")
	}
	if f.criteria(t) != criteria {
		t.Fatal("correction changed criteria without ticket write permission")
	}
	f.advance(time.Minute)
	f.dispatch(t)
	if f.count(t, `SELECT count(*) FROM events WHERE type='question.outcome_applied' AND after->>'effect_id'=$1`, current.ID) != 1 {
		t.Fatal("correction replay duplicated the effect")
	}
}
