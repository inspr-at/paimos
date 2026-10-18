// SPDX-License-Identifier: AGPL-3.0-only

package goals

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/eta"
	"github.com/inspr-at/paimos/internal/fieldschema"
)

var fixedNow = time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)

// Risk: malformed or oversized updates allocate unbounded work or lose a
// missing/null distinction before a multi-node transaction can validate it.
func TestUpdateValidationBounds(t *testing.T) {
	upserts := make([]string, 201)
	for i := range upserts {
		upserts[i] = fmt.Sprintf(`{"ref":"outcome-%d","label":"Outcome"}`, i)
	}
	keys := make([]string, 21)
	for i := range keys {
		keys[i] = fmt.Sprintf(`"AEON-%d"`, i+1)
	}
	cases := []struct{ name, body, path string }{
		{"missing ref", `{"items":{"upsert":[{"label":"No ref"}]}}`, "items.upsert[0].ref"},
		{"blank ref", `{"items":{"upsert":[{"ref":""}]}}`, "items.upsert[0].ref"},
		{"duplicate ref", `{"items":{"upsert":[{"ref":"r"},{"ref":"r"}]}}`, "items.upsert[1].ref"},
		{"upsert and remove", `{"items":{"upsert":[{"ref":"r"}],"remove":["r"]}}`, "items.remove[0]"},
		{"operation limit", `{"items":{"upsert":[` + strings.Join(upserts, ",") + `]}}`, "items"},
		{"target limit", `{"items":{"upsert":[{"ref":"r","keys":[` + strings.Join(keys, ",") + `]}]}}`, "items.upsert[0].keys"},
		{"two current steps", `{"steps":[{"id":"a","text":"A","state":"now"},{"id":"b","text":"B","state":"now"}]}`, "steps[1].state"},
		{"duplicate step", `{"steps":[{"id":"a","text":"A","state":"later"},{"id":"a","text":"B","state":"done"}]}`, "steps[1].id"},
		{"replace needs watermark", `{"items":{"mode":"replace"}}`, "if_read_through"},
		{"null watermark", `{"if_read_through":null,"items":{"mode":"replace"}}`, "if_read_through"},
		{"negative watermark", `{"if_read_through":-1}`, "if_read_through"},
		{"bad mode", `{"items":{"mode":"append"}}`, "items.mode"},
		{"null items", `{"items":null}`, "items"},
		{"no clock or text", `{"eta":{}}`, "eta.text"},
		{"ETA future bound", `{"eta":{"at":"2028-10-16T20:00:00Z"}}`, "eta.at"},
		{"ETA past bound", `{"eta":{"at":"2025-10-16T20:00:00Z"}}`, "eta.at"},
		{"ETA text length", `{"eta":{"text":"` + strings.Repeat("ä", 41) + `"}}`, "eta.text"},
		{"label clear", `{"items":{"upsert":[{"ref":"r","label":null}]}}`, "items.upsert[0].label"},
		{"label length", `{"items":{"upsert":[{"ref":"r","label":"` + strings.Repeat("a", 301) + `"}]}}`, "items.upsert[0].label"},
		{"note length", `{"items":{"upsert":[{"ref":"r","note":"` + strings.Repeat("a", 201) + `"}]}}`, "items.upsert[0].note"},
		{"state typo", `{"items":{"upsert":[{"ref":"r","state":"working"}]}}`, "items.upsert[0].state"},
		{"position bound", `{"items":{"upsert":[{"ref":"r","position":1e15}]}}`, "items.upsert[0].position"},
		{"position exponent bound", `{"items":{"upsert":[{"ref":"r","position":1e999999999}]}}`, "items.upsert[0].position"},
		{"position precision", `{"items":{"upsert":[{"ref":"r","position":0.0000000000000001}]}}`, "items.upsert[0].position"},
		{"position string", `{"items":{"upsert":[{"ref":"r","position":"1"}]}}`, "body"},
		{"bad target", `{"items":{"upsert":[{"ref":"r","keys":["unknown"]}]}}`, "items.upsert[0].keys[0]"},
		{"duplicate target", `{"items":{"upsert":[{"ref":"r","keys":["AEON-1","AEON-1"]}]}}`, "items.upsert[0].keys[1]"},
		{"move by ref", `{"items":{"move":[{"outcome_id":"r","to_goal":"GOAL-2"}]}}`, "items.move[0].outcome_id"},
		{"move invalid goal", `{"items":{"move":[{"outcome_id":"d7b72059-aa74-444c-8132-ce4ebf56ba47","to_goal":"workspace"}]}}`, "items.move[0].to_goal"},
		{"move invalid ref", `{"items":{"move":[{"outcome_id":"d7b72059-aa74-444c-8132-ce4ebf56ba47","to_goal":"GOAL-2","to_ref":"UPPER"}]}}`, "items.move[0].to_ref"},
		{"missing override pct", `{"progress_override":{}}`, "progress_override.pct"},
		{"override bound", `{"progress_override":{"pct":101}}`, "progress_override.pct"},
		{"handover detail bound", `{"items":{"handed_over":{"ref":"r","detail":"` + strings.Repeat("a", 2001) + `"}}}`, "items.handed_over.detail"},
		{"unknown root", `{"other":1}`, "body"},
		{"unknown optional field", `{"eta":{"other":1}}`, "body"},
		{"unknown upsert field", `{"items":{"upsert":[{"ref":"r","other":1}]}}`, "body"},
		{"duplicate JSON field", `{"eta":{"text":"A","text":"B"}}`, "body.eta"},
		{"duplicate case alias", `{"eta":{"text":"A"},"ETA":{"text":"B"}}`, "body"},
		{"malformed JSON", `{"eta":{`, "body.eta"},
		{"trailing document", `{} {}`, "body"},
		{"top level null", `null`, "body"},
		{"byte limit", `{"eta":{"text":"` + strings.Repeat("a", MaxBodyBytes) + `"}}`, "body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeUpdate(strings.NewReader(tc.body), fixedNow)
			var validation *ValidationError
			if !errors.As(err, &validation) || validation.Path != tc.path {
				t.Fatalf("got %v, want validation at %s", err, tc.path)
			}
		})
	}
	// These are valid clears, in contrast to missing refs and empty ETA objects.
	valid := `{"if_read_through":0,"eta":null,"steps":null,"progress_override":null,"items":{"mode":"replace","upsert":[{"ref":"r","label":"` + strings.Repeat("ä", 300) + `","state":null,"keys":null,"eta":{"text":"` + strings.Repeat("ä", 40) + `"}}],"handed_over":null}}`
	if _, err := DecodeUpdate(strings.NewReader(valid), fixedNow); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{fixedNow.Add(-eta.MaxPast), fixedNow.Add(eta.MaxFuture)} {
		body := fmt.Sprintf(`{"eta":{"at":%q}}`, at.Format(time.RFC3339))
		if _, err := DecodeUpdate(strings.NewReader(body), fixedNow); err != nil {
			t.Fatalf("allowed ETA boundary: %v", err)
		}
	}
}

// Risk: retries hash object serialization order, or conflate a clear with an
// omitted field and return the wrong stored mutation result.
func TestUpdateDigestRetainsPatchIntent(t *testing.T) {
	parse := func(body string) Update {
		t.Helper()
		update, err := DecodeUpdate(strings.NewReader(body), fixedNow)
		if err != nil {
			t.Fatal(err)
		}
		return update
	}
	digest := func(update Update) [32]byte {
		t.Helper()
		digest, err := update.Digest()
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}
	a := parse(`{"eta":{"text":"~21:00"},"items":{"upsert":[{"ref":"r","state":null}]}}`)
	b := parse(`{"items":{"upsert":[{"state":null,"ref":"r"}]},"eta":{"text":"~21:00"}}`)
	if digest(a) != digest(b) {
		t.Fatal("member order changed digest")
	}
	if digest(parse(`{}`)) == digest(parse(`{"eta":null}`)) {
		t.Fatal("omitted and cleared ETA have the same digest")
	}
	if digest(a) == digest(parse(`{"eta":{"text":"~21:00"},"items":{"upsert":[{"ref":"r"}]}}`)) {
		t.Fatal("omitted and cleared outcome state have the same digest")
	}
	var raw map[string]json.RawMessage
	bytes, err := json.Marshal(a)
	if err != nil || json.Unmarshal(bytes, &raw) != nil || len(raw) != 2 {
		t.Fatalf("canonical body includes absent fields: %s (%v)", bytes, err)
	}
	if !a.Items.Value.Upsert[0].State.Set || a.Items.Value.Upsert[0].State.Value != nil || a.Steps.Set {
		t.Fatal("patch intent lost")
	}
	positions := func(value string) Update {
		return parse(`{"items":{"upsert":[{"ref":"r","position":` + value + `}]}}`)
	}
	if digest(positions("1")) != digest(positions("1.0")) || digest(positions("1")) != digest(positions("1e0")) {
		t.Fatal("equivalent decimal positions have different canonical digests")
	}
	if digest(positions("999999999999999.000000000000001")) == digest(positions("999999999999999.000000000000002")) {
		t.Fatal("different positions collapsed to float64 precision in the digest")
	}
}

func TestGoalIdempotencyKeyGrammar(t *testing.T) {
	for _, key := range []string{"12345678", "  goal:call/123_4-a.b  ", strings.Repeat("x", 200)} {
		if got, err := CleanIdempotencyKey(key); err != nil || got != strings.TrimSpace(key) {
			t.Fatalf("valid key rejected: %q %v", key, err)
		}
	}
	for _, key := range []string{"", "1234567", strings.Repeat("x", 201), "auto:12345678", "/12345678", "some space", "key\n12345", "äpple1234"} {
		if _, err := CleanIdempotencyKey(key); err == nil {
			t.Fatalf("invalid key accepted: %q", key)
		}
	}
}

// Risk: queued/handed-over hides a worker, or an empty leaf set derives Done.
func TestGoalStateWorkerWins(t *testing.T) {
	cases := []struct {
		name   string
		facts  StateFacts
		state  State
		reason string
		badge  bool
	}{
		{"worker queued", StateFacts{SessionIDs: []string{"s"}, StateSet: Queued}, Moving, "", true},
		{"worker handed over", StateFacts{SessionIDs: []string{"s"}, HandedOver: true}, Moving, "", true},
		{"worker done leaves", StateFacts{SessionIDs: []string{"s"}, TotalLeaves: 1, DoneLeaves: 1}, Moving, "", true},
		{"stopped worker", StateFacts{StateSet: Queued}, Queued, "", false},
		{"handed over", StateFacts{HandedOver: true}, Done, "", false},
		{"nonempty done", StateFacts{TotalLeaves: 1, DoneLeaves: 1, StateSet: Moving}, Done, "", false},
		{"empty leaves", StateFacts{}, Queued, "waiting", false},
		{"explicit before review", StateFacts{StateSet: Paused, Review: true}, Paused, "", false},
		{"review", StateFacts{Review: true, Paused: true}, Moving, "", false},
		{"PR", StateFacts{OpenPR: true}, Moving, "", false},
		{"paused", StateFacts{Paused: true, Queued: true}, Paused, "", false},
		{"start queue", StateFacts{Queued: true, Blocked: true}, Queued, "start_queue", false},
		{"blocked", StateFacts{Blocked: true}, Queued, "blocked", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveState(tc.facts)
			if got.State != tc.state || got.Reason != tc.reason || (len(got.SessionIDs) > 0) != tc.badge || got.SessionIDs == nil {
				t.Fatalf("got %+v", got)
			}
		})
	}
	facts := StateFacts{SessionIDs: []string{"b", "a", "b", ""}}
	if got := DeriveState(facts).SessionIDs; !reflect.DeepEqual(got, []string{"a", "b"}) || facts.SessionIDs[0] != "b" {
		t.Fatalf("badge is not unique/stable, or modified input: %v", got)
	}
}

// Risk: partial estimates are counted as complete/unestimated, unknown leaves
// receive invented hours, or shared leaves get double weight.
func TestGoalRollupPartialAndSharedEstimates(t *testing.T) {
	partial := ProgressOutcome{State: Done, Leaves: []LeafEstimate{{"a", 8}, {"b", 0}, {"c", 0}, {"d", 0}, {"e", 0}}}
	got := ComputeRollup([]ProgressOutcome{partial, {State: Queued, Leaves: []LeafEstimate{{"f", 2}}}, {State: Queued}})
	if got.Pct != 53 || got.Basis != "mixed" || got.Done != 1 || got.Total != 3 || got.ItemsPartiallyEstimated != 1 || got.ItemsWithoutEstimate != 1 {
		t.Fatalf("partial coverage: %+v", got)
	}
	if got.Outcomes[0].Coverage != (Coverage{1, 5}) || got.Outcomes[0].Hours != 8 || got.Outcomes[2].Hours != 5 {
		t.Fatalf("unknown hours were filled or mean was wrong: %+v", got.Outcomes)
	}
	shared := ComputeRollup([]ProgressOutcome{
		{State: Done, Leaves: []LeafEstimate{{"shared", 8}, {"shared", 8}}},
		{State: Queued, Leaves: []LeafEstimate{{"shared", 8}, {"unique", 2}}},
	})
	if shared.Pct != 40 || shared.Basis != "estimate" || shared.Outcomes[0].Hours != 4 || shared.Outcomes[1].Hours != 6 || shared.Outcomes[0].Coverage.TotalLeaves != 1 {
		t.Fatalf("shared leaf weighting: %+v", shared)
	}
	count := ComputeRollup([]ProgressOutcome{{State: Done, Leaves: []LeafEstimate{{"a", math.NaN()}, {"b", math.Inf(1)}, {"c", 201}}}, {State: Queued}})
	if count.Pct != 50 || count.Basis != "count" || count.ItemsWithoutEstimate != 2 {
		t.Fatalf("unestimated count basis: %+v", count)
	}
	empty := ComputeRollup(nil)
	if empty.Pct != 0 || empty.Total != 0 || empty.Done != 0 || empty.Basis != "count" {
		t.Fatalf("empty goal: %+v", empty)
	}
}

// Risk: report intervals change Passed, equality uses the wrong boundary, or
// equal event watermarks let an older time-driven response replace a newer one.
func TestGoalClockBoundariesAndWatermarks(t *testing.T) {
	at := fixedNow.Add(-10 * time.Minute)
	if Passed(&at, Moving, fixedNow) || !Passed(&at, Moving, fixedNow.Add(time.Second)) || Passed(&at, Done, fixedNow.Add(time.Hour)) || Passed(nil, Moving, fixedNow) {
		t.Fatal("passed must be strictly after ETA + fixed 10 minutes, and never done")
	}
	since := fixedNow.Add(-4 * time.Hour)
	if DeriveStall(1, since, fixedNow.Add(-time.Second)).Stalled || !DeriveStall(1, since, fixedNow).Stalled || DeriveStall(0, since, fixedNow).Stalled {
		t.Fatal("stall boundary must be inclusive 4 hours and require Moving")
	}
	old, current := (Watermark{ReadThrough: 41, Generation: 7}), (Watermark{ReadThrough: 41, Generation: 8})
	if !old.OlderThan(current) || current.OlderThan(old) || current.OlderThan(current) || (Watermark{ReadThrough: 42, Generation: 1}).OlderThan(current) {
		t.Fatal("watermark/generation ordering is wrong")
	}
	var ping map[string]any
	raw, err := json.Marshal(ChangePing{GoalID: "goal", Watermark: current})
	if err != nil || json.Unmarshal(raw, &ping) != nil || len(ping) != 3 || ping["goal_id"] != "goal" || ping["read_through"] != float64(41) || ping["generation"] != float64(8) {
		t.Fatalf("derived event must be a value-free three-field ping: %s %v", raw, err)
	}
}

func TestGoalKindSchemasAndNextStep(t *testing.T) {
	for _, slug := range []string{GoalKind, OutcomeKind} {
		schema, err := fieldschema.Compile(kindFieldSchema(slug))
		if err != nil {
			t.Fatalf("kind %s is unsupported by node field validation: %v", slug, err)
		}
		var fieldSchema map[string]any
		if err := json.Unmarshal(kindFieldSchema(slug), &fieldSchema); err != nil {
			t.Fatal(err)
		}
		if _, workKind := fieldSchema["issue_family"]; workKind {
			t.Fatal("goal/outcome became a work kind")
		}
		fields := map[string]any{"eta_text": "Tomorrow"}
		if slug == OutcomeKind {
			if err := schema.Validate(fields); err == nil {
				t.Fatal("outcome schema accepts a missing ref")
			}
			fields["outcome_ref"] = "r"
		}
		if err := schema.Validate(fields); err != nil {
			t.Fatal(err)
		}
		fields["eta_text"] = strings.Repeat("a", 41)
		if err := schema.Validate(fields); err == nil {
			t.Fatal("stored ETA exceeds schema bound")
		}
	}
	steps := []Step{{"done", "Done", "done"}, {"later", "Later", "later"}, {"now", "Current", "now"}}
	if next := NextStep(steps); next == nil || next.ID != "now" {
		t.Fatal("current step does not win")
	}
	if next := NextStep(steps[:2]); next == nil || next.ID != "later" || NextStep(steps[:1]) != nil {
		t.Fatal("later-step fallback is wrong")
	}
	if !IsGoalKind("goal") || !IsGoalKind("release") || IsGoalKind("outcome") || IsGoalKind("work") {
		t.Fatal("goal vocabulary changed")
	}
}
