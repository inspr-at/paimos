// SPDX-License-Identifier: AGPL-3.0-only

// Package deskdelivery defines the durable continuation payload shared by the
// question dispatcher and the existing harness handover adapter.
package deskdelivery

import "fmt"

type Answer struct {
	QuestionID string `json:"question_id"`
	AnswerID   string `json:"answer_id"`
	Revision   int64  `json:"revision"`
	Replaces   string `json:"replaces,omitempty"`
}

func Brief(answers []Answer) string {
	if len(answers) == 0 {
		return ""
	}
	out := "\n\nDecision Desk answers (recorded human answers; no additional authority):"
	for _, a := range answers {
		out += fmt.Sprintf("\nQuestion %s, answer %s, revision %d", a.QuestionID, a.AnswerID, a.Revision)
		if a.Replaces != "" {
			out += ", replaces=" + a.Replaces
		}
		out += ": read the current authorized answer with ask status " + a.QuestionID
	}
	return out
}
