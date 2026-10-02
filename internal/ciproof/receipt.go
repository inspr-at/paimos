// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"fmt"
	"strings"
	"time"
)

// Receipt reserves execution provenance for the external supervisor in B/E.
// In A it is an unsigned shadow observation; it cannot satisfy an obligation.
type Receipt struct {
	Schema            string            `json:"schema"`
	PlanID            string            `json:"plan_id"`
	ObligationID      string            `json:"obligation_id"`
	Fingerprint       string            `json:"fingerprint"`
	EnvironmentDigest string            `json:"environment_digest"`
	CandidateCommit   string            `json:"candidate_commit"`
	CandidateTree     string            `json:"candidate_tree"`
	PolicyDigest      string            `json:"policy_digest"`
	WorkflowCommit    string            `json:"workflow_commit"`
	ExecutorDigest    string            `json:"executor_digest"`
	RunID             int64             `json:"run_id"`
	Attempt           int64             `json:"attempt"`
	JobID             string            `json:"job_id"`
	StartedAt         string            `json:"started_at"`
	CompletedAt       string            `json:"completed_at"`
	Result            string            `json:"result"`
	Manifest          ExecutionManifest `json:"manifest"`
	ArtifactDigests   []string          `json:"artifact_digests"`
}

type ExecutionManifest struct {
	Kind     string   `json:"kind"`
	Expected []string `json:"expected"`
	Executed []string `json:"executed"`
	Skipped  []string `json:"skipped"`
}

// ValidateReceipt checks structural consistency only. Expected identities here
// are observations until an approved supervisor independently fixes them in B.
// Even a valid success leaves the shadow plan pending and all actions set to run.
func ValidateReceipt(p Plan, r Receipt) error {
	if err := ValidatePlan(p); err != nil {
		return err
	}
	if err := validateContract("receipt", r); err != nil {
		return err
	}
	if r.PlanID != p.ID || r.EnvironmentDigest != p.EnvironmentDigest || r.CandidateCommit != p.Candidate.Commit || r.CandidateTree != p.Candidate.Tree || r.PolicyDigest != p.Policy.Digest || r.WorkflowCommit != p.Policy.Commit {
		return fmt.Errorf("receipt plan/input/provenance mismatch")
	}
	var unit *Obligation
	for i := range p.Obligations {
		if p.Obligations[i].ID == r.ObligationID {
			unit = &p.Obligations[i]
			break
		}
	}
	if unit == nil || r.Fingerprint != unit.Fingerprint {
		return fmt.Errorf("receipt obligation mismatch")
	}
	start, e1 := time.Parse(time.RFC3339Nano, r.StartedAt)
	end, e2 := time.Parse(time.RFC3339Nano, r.CompletedAt)
	if e1 != nil || e2 != nil || !end.After(start) {
		return fmt.Errorf("receipt terminal time invalid")
	}
	if !sortedUnique(r.Manifest.Expected) {
		return fmt.Errorf("empty or duplicate execution inventory")
	}
	if len(r.Manifest.Executed) > 0 && !sortedUnique(r.Manifest.Executed) {
		return fmt.Errorf("duplicate execution result")
	}
	if len(r.Manifest.Skipped) > 0 && !sortedUnique(r.Manifest.Skipped) {
		return fmt.Errorf("duplicate skipped identity")
	}
	allowed := map[string]bool{}
	seen := map[string]bool{}
	for _, id := range r.Manifest.Expected {
		allowed[id] = true
	}
	for _, ids := range [][]string{r.Manifest.Executed, r.Manifest.Skipped} {
		for _, id := range ids {
			if !allowed[id] || seen[id] {
				return fmt.Errorf("unexpected or contradictory execution identity")
			}
			seen[id] = true
		}
	}
	if r.Result == "success" && (len(r.Manifest.Skipped) != 0 || strings.Join(r.Manifest.Executed, "\x00") != strings.Join(r.Manifest.Expected, "\x00")) {
		return fmt.Errorf("success requires complete fresh execution")
	}
	if unit.Kind != "job" && r.Manifest.Kind != "test" {
		return fmt.Errorf("test obligation requires test manifest")
	}
	return nil
}
