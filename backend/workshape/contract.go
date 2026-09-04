// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package workshape owns the closed, value-free contract that distinguishes
// product-changing ship assignments from evidence-producing scout investigations.
package workshape

const (
	Unknown = "unknown"
	Ship    = "ship"
	Scout   = "scout"

	OutputUnclassified          = "unclassified"
	OutputDelivery              = "delivery"
	OutputInvestigationEvidence = "investigation_evidence"
)

var stages = []string{"specification", "implementation", "qa", "deployment", "verification"}

// StageApplicability is one ordered default. It does not rewrite an existing
// delivery attempt: attempt policy rows remain the immutable delivery truth.
type StageApplicability struct {
	Stage         string `json:"stage"`
	Applicability string `json:"applicability"`
}

// Contract is deliberately bounded and value-free. It contains no prompt,
// repository path, ticket prose, evidence body, or provider output.
type Contract struct {
	Shape              string               `json:"shape"`
	OutputKind         string               `json:"output_kind"`
	StageApplicability []StageApplicability `json:"stage_applicability"`
	DefinitionOfDone   []string             `json:"definition_of_done"`
	NonGoals           []string             `json:"non_goals"`
}

func ValidPersisted(value string) bool { return value == Ship || value == Scout }

// Normalize projects absent legacy storage as unknown without inferring from
// role, ticket, prompt, workspace, delivery state, or repository activity.
func Normalize(value string) string {
	if ValidPersisted(value) {
		return value
	}
	return Unknown
}

func stageDefaults(required map[string]bool) []StageApplicability {
	out := make([]StageApplicability, 0, len(stages))
	for _, stage := range stages {
		applicability := "not_applicable"
		if required[stage] {
			applicability = "required"
		}
		out = append(out, StageApplicability{Stage: stage, Applicability: applicability})
	}
	return out
}

// For returns a fresh contract so callers cannot mutate process-global
// defaults. Scout requires only framing and evidence verification; code,
// product QA, and deployment stages are not applicable to an investigation.
func For(value string) Contract {
	switch Normalize(value) {
	case Ship:
		return Contract{
			Shape: Ship, OutputKind: OutputDelivery,
			StageApplicability: stageDefaults(map[string]bool{
				"specification": true, "implementation": true, "qa": true, "deployment": true, "verification": true,
			}),
			DefinitionOfDone: []string{"implementation_complete", "required_stages_succeeded", "delivery_verified"},
			NonGoals:         []string{"git_enforcement"},
		}
	case Scout:
		return Contract{
			Shape: Scout, OutputKind: OutputInvestigationEvidence,
			StageApplicability: stageDefaults(map[string]bool{
				"specification": true, "verification": true,
			}),
			DefinitionOfDone: []string{"investigation_question_answered", "evidence_recorded", "uncertainty_reported"},
			NonGoals:         []string{"product_delivery", "git_enforcement", "silent_promotion"},
		}
	default:
		out := make([]StageApplicability, 0, len(stages))
		for _, stage := range stages {
			out = append(out, StageApplicability{Stage: stage, Applicability: Unknown})
		}
		return Contract{
			Shape: Unknown, OutputKind: OutputUnclassified, StageApplicability: out,
			DefinitionOfDone: []string{"explicit_classification_required"},
			NonGoals:         []string{"delivery_claim", "git_enforcement", "shape_inference"},
		}
	}
}
