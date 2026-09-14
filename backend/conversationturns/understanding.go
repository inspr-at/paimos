// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package conversationturns

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// UnderstandingOutputSchema is owned by the server and mirrors the current
// public Aithema understanding snapshot contract. Callers cannot replace it.
func UnderstandingOutputSchema() map[string]any {
	stringArray := func(max int) map[string]any {
		return map[string]any{"type": "array", "maxItems": max, "items": map[string]any{"type": "string", "minLength": 1}}
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"summary", "facts", "open_questions", "next_question", "candidate_requirements", "project_kinds"},
		"properties": map[string]any{
			"summary": map[string]any{"type": "string", "minLength": 1, "maxLength": 4000},
			"facts": map[string]any{"type": "array", "maxItems": 12, "items": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"key", "value", "evidence"},
				"properties": map[string]any{"key": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "value": map[string]any{"type": "string", "minLength": 1, "maxLength": 1000}, "evidence": map[string]any{"type": "string", "maxLength": 1000}},
			}},
			"open_questions": stringArray(24),
			"next_question":  map[string]any{"type": "string", "maxLength": 500},
			"candidate_requirements": map[string]any{"type": "array", "maxItems": 12, "items": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"requirement_ref", "statement", "acceptance_criteria", "constraint_refs"},
				"properties": map[string]any{"requirement_ref": map[string]any{"type": "string", "minLength": 1}, "statement": map[string]any{"type": "string", "minLength": 1}, "acceptance_criteria": stringArray(64), "constraint_refs": stringArray(64)},
			}},
			"project_kinds": map[string]any{"type": "array", "minItems": 1, "uniqueItems": true, "items": map[string]any{"type": "string", "enum": []string{"new_product", "iteration", "integration"}}},
		},
	}
}

type understanding struct {
	Summary               string                     `json:"summary"`
	Facts                 []understandingFact        `json:"facts"`
	OpenQuestions         []string                   `json:"open_questions"`
	NextQuestion          string                     `json:"next_question"`
	CandidateRequirements []understandingRequirement `json:"candidate_requirements"`
	ProjectKinds          []string                   `json:"project_kinds"`
}

type understandingFact struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Evidence string `json:"evidence"`
}

type understandingRequirement struct {
	RequirementRef     string   `json:"requirement_ref"`
	Statement          string   `json:"statement"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	ConstraintRefs     []string `json:"constraint_refs"`
}

func validateUnderstandingOutput(raw string) error {
	dec := json.NewDecoder(bytes.NewBufferString(raw))
	dec.DisallowUnknownFields()
	var value understanding
	if err := dec.Decode(&value); err != nil {
		return ErrInvalid
	}
	if err := dec.Decode(&json.RawMessage{}); err != io.EOF || strings.TrimSpace(value.Summary) == "" || len(value.Summary) > 4000 ||
		value.Facts == nil || len(value.Facts) > 12 || value.OpenQuestions == nil || len(value.OpenQuestions) > 24 ||
		len(value.NextQuestion) > 500 || value.CandidateRequirements == nil || len(value.CandidateRequirements) > 12 ||
		len(value.ProjectKinds) == 0 || len(value.ProjectKinds) > 3 {
		return ErrInvalid
	}
	for _, fact := range value.Facts {
		if strings.TrimSpace(fact.Key) == "" || len(fact.Key) > 80 || strings.TrimSpace(fact.Value) == "" || len(fact.Value) > 1000 || len(fact.Evidence) > 1000 {
			return ErrInvalid
		}
	}
	for _, question := range value.OpenQuestions {
		if strings.TrimSpace(question) == "" || len(question) > 500 {
			return ErrInvalid
		}
	}
	for _, requirement := range value.CandidateRequirements {
		if strings.TrimSpace(requirement.RequirementRef) == "" || strings.TrimSpace(requirement.Statement) == "" || len(requirement.AcceptanceCriteria) == 0 || requirement.ConstraintRefs == nil {
			return ErrInvalid
		}
		for _, item := range append(append([]string{}, requirement.AcceptanceCriteria...), requirement.ConstraintRefs...) {
			if strings.TrimSpace(item) == "" {
				return ErrInvalid
			}
		}
	}
	seen := map[string]bool{}
	for _, kind := range value.ProjectKinds {
		if seen[kind] || (kind != "new_product" && kind != "iteration" && kind != "integration") {
			return ErrInvalid
		}
		seen[kind] = true
	}
	return nil
}
