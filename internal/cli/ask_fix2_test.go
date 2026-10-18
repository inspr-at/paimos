// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/questions"
)

func TestFix2QuestionStatusUsesCurrentDoctrineLifecycle(t *testing.T) {
	for _, state := range []string{"pending", "dismissed", "proposed", "promoted", ""} {
		t.Run(state, func(t *testing.T) {
			var q questions.Question
			raw, _ := json.Marshal(map[string]any{"answer": map[string]string{"outcome": "doctrine"}, "pending": []map[string]any{{"kind": "outcome", "state": "delivered", "effect_ref": "draft", "doctrine_state": state}}})
			if err := json.Unmarshal(raw, &q); err != nil {
				t.Fatal(err)
			}
			out := &bytes.Buffer{}
			if err := (&runtime{stdout: out}).printQuestion(q); err != nil {
				t.Fatal(err)
			}
			waiting := strings.Contains(out.String(), "draft saved; waiting for a person")
			if waiting != (state == "pending") {
				t.Fatalf("misleading lifecycle %s: %s", state, out.String())
			}
			if state == "dismissed" && !strings.Contains(out.String(), "dismissed or expired") {
				t.Fatal("dismissal hidden")
			}
			if state == "proposed" || state == "promoted" {
				if !strings.Contains(out.String(), "proposal: "+state) {
					t.Fatal("publication state hidden")
				}
			}
		})
	}
}

func TestFix2QuestionStatusExplainsPersonReview(t *testing.T) {
	var q questions.Question
	if err := json.Unmarshal([]byte(`{"answer":{"outcome":"once"},"pending":[{"kind":"outcome","state":"delivered","effect_data":{"review_required":[{"kind":"doctrine","ref":"old-draft","why":"Published proposal preserved."}]}}]}`), &q); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := (&runtime{stdout: out}).printQuestion(q); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "person review required") || !strings.Contains(out.String(), "old-draft") || !strings.Contains(out.String(), "Published proposal preserved.") {
		t.Fatalf("review hidden: %s", out.String())
	}
}
