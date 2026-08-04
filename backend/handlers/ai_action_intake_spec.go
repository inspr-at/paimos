// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

// PAI-705 (epic PAI-703). The intake_spec action — the voice-intake
// workbench's continuous spec generator. One call regenerates the full
// specification from (previous spec + newest transcript material) and
// derives the ticket-preview fields, so the spec and the preview can
// never disagree.
//
// Called two ways:
//   - by the intake orchestrator (the normal path): ax.Text carries the
//     transcript tail, ax.Params carries {prior_spec, language}.
//   - via the POST /api/ai/action dispatcher with plain text: Params may
//     be absent; the handler treats Text as a fresh transcript.

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func init() {
	replaceAction(actionDescriptor{
		Key:         "intake_spec",
		Label:       "Intake: live specification",
		Surface:     "intake",
		Placement:   "text",
		Handler:     intakeSpecHandler,
		Implemented: true,
	})
}

type intakeSpecParams struct {
	PriorSpec string `json:"prior_spec"`
	Language  string `json:"language"`
	// PAI-708: top retrieval issue hits from the target project so the
	// model can categorize relations against real issues.
	CandidateIssues []intakeSpecCandidateIssue `json:"candidate_issues,omitempty"`
}

type intakeSpecCandidateIssue struct {
	IssueKey string `json:"issue_key"`
	Title    string `json:"title"`
}

type intakeSpecRelation struct {
	IssueKey string `json:"issue_key"`
	Category string `json:"category"` // touches | conflicts | extends | related
}

type intakeSpecBody struct {
	Markdown           string               `json:"markdown"`
	Title              string               `json:"title"`
	IssueType          string               `json:"issue_type"`
	Description        string               `json:"description"`
	AcceptanceCriteria string               `json:"acceptance_criteria"`
	Relations          []intakeSpecRelation `json:"relations"`
}

func intakeSpecHandler(ax *aiActionContext) (any, string, int, int, string, error) {
	if strings.TrimSpace(ax.Text) == "" {
		return nil, "", 0, 0, "", &userError{status: 400, msg: "intake_spec requires transcript text"}
	}
	var params intakeSpecParams
	if len(ax.Params) > 0 {
		_ = json.Unmarshal(ax.Params, &params)
	}
	lang := params.Language
	if lang != "de" {
		lang = "en"
	}

	systemPrompt := resolveActionPromptWithPreset(ax, "intake_spec")

	var u strings.Builder
	fmt.Fprintf(&u, "Requested language: %s\n\n", lang)
	u.WriteString("PREVIOUS SPECIFICATION:\n")
	if strings.TrimSpace(params.PriorSpec) == "" {
		u.WriteString("(empty — this is the start of the session)\n")
	} else {
		u.WriteString(params.PriorSpec)
		u.WriteString("\n")
	}
	u.WriteString("\nNEWEST TRANSCRIPT MATERIAL:\n")
	u.WriteString(ax.Text)
	if len(params.CandidateIssues) > 0 {
		u.WriteString("\n\nEXISTING ISSUES in the target project (categorize a relation ONLY when the idea genuinely touches, conflicts with, or extends one — omit the rest):\n")
		for _, c := range params.CandidateIssues {
			fmt.Fprintf(&u, "- %s: %s\n", c.IssueKey, c.Title)
		}
	}
	u.WriteString("\n\nRegenerate the complete specification and the ticket preview. Return the JSON object.")

	ctx, cancel := context.WithTimeout(ax.Ctx, 75*time.Second)
	defer cancel()
	var body intakeSpecBody
	model, ptok, ctok, finish, err := callJSONAction(ctx, ax, systemPrompt, u.String(), 2800, &body)
	if err != nil {
		return nil, model, ptok, ctok, finish, err
	}
	switch body.IssueType {
	case "ticket", "epic", "task":
	default:
		body.IssueType = "ticket"
	}
	// Relations must reference candidate issues only, with known categories.
	allowed := map[string]bool{}
	for _, c := range params.CandidateIssues {
		allowed[c.IssueKey] = true
	}
	kept := body.Relations[:0]
	for _, rel := range body.Relations {
		switch rel.Category {
		case "touches", "conflicts", "extends", "related":
		default:
			continue
		}
		if allowed[rel.IssueKey] {
			kept = append(kept, rel)
		}
	}
	body.Relations = kept
	if len(body.Markdown) > intakeSpecMaxBytes {
		body.Markdown = body.Markdown[:intakeSpecMaxBytes]
	}
	return body, model, ptok, ctok, finish, nil
}
