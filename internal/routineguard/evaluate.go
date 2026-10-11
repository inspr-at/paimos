// SPDX-License-Identifier: AGPL-3.0-only
package routineguard

import (
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

type ArtifactChange struct {
	BeforePath string `json:"before_path,omitempty"`
	AfterPath  string `json:"after_path,omitempty"`
}

// Paths must include resolved targets; artifact renames/copies include both
// sides. The workspace broker separately enforces symlink/worktree confinement.
type Context struct {
	Checkpoint        string           `json:"checkpoint"`
	Action            string           `json:"action,omitempty"`
	Text              string           `json:"text"`
	Paths             []string         `json:"paths"`
	Artifacts         []ArtifactChange `json:"artifacts,omitempty"`
	PayloadBytes      int64            `json:"payload_bytes"`
	ChangesGuardrails bool             `json:"changes_guardrails"`
}
type Finding struct {
	RuleID  string  `json:"rule_id"`
	Version string  `json:"version"`
	Level   string  `json:"level"`
	Scope   Scope   `json:"scope"`
	Result  Outcome `json:"result"`
	Reason  string  `json:"reason"`
}
type EvaluationRequirement struct {
	RuleID          string `json:"rule_id"`
	Version         string `json:"version"`
	PolicyDigest    string `json:"policy_digest"`
	ContextDigest   string `json:"context_digest"`
	DifferentFamily bool   `json:"different_family"`
}
type Decision struct {
	// Only S08's verified verdict path in this package may set this latch.
	// Serialized or reconstructed metadata cannot grant action execution.
	evaluationVerified  string
	evaluationBinding   string
	Checkpoint          string                 `json:"checkpoint"`
	Result              Outcome                `json:"result"`
	DeterministicResult Outcome                `json:"deterministic_result"`
	PolicyDigest        string                 `json:"policy_digest"`
	ContextDigest       string                 `json:"context_digest"`
	HardVersion         string                 `json:"hard_version"`
	PresetVersion       string                 `json:"preset_version"`
	Findings            []Finding              `json:"findings"`
	RequiredEvaluation  *EvaluationRequirement `json:"required_evaluation,omitempty"`
}

// CanExecute is fail closed. S08 must verify and bind the independent verdict
// before action execution; a deterministic allow never substitutes for it.
func (d Decision) CanExecute() bool {
	return d.evaluationBinding != "" && d.evaluationVerified != "" && d.evaluationVerified == Digest(d) && d.Checkpoint == "action" && d.Result == Allow && d.RequiredEvaluation != nil && d.RequiredEvaluation.RuleID == "hard.different_family_v1" && d.RequiredEvaluation.Version == HardVersion && d.RequiredEvaluation.PolicyDigest == d.PolicyDigest && d.RequiredEvaluation.ContextDigest == d.ContextDigest && d.HardVersion == HardVersion
}
func validatePath(p string) error {
	if p == "" || len(p) > MaxPathBytes || !utf8.ValidString(p) || strings.ContainsAny(p, "\\\x00\r\n:%?#") || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return fmt.Errorf("guardrail paths must be bounded canonical repository-relative paths")
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == ".." || segment == "." || strings.EqualFold(segment, ".git") {
			return fmt.Errorf("unsafe guardrail path")
		}
	}
	return nil
}
func ValidateContext(c Context) error {
	if (c.Checkpoint != "save" && c.Checkpoint != "action") || len(c.Action) > 80 || len(c.Text) > MaxTextBytes || !utf8.ValidString(c.Text) || c.PayloadBytes < 0 || c.PayloadBytes > MaxTextBytes || len(c.Paths) > MaxPaths || len(c.Artifacts) > MaxPaths {
		return fmt.Errorf("guardrail context exceeds limits or has an invalid checkpoint")
	}
	bytes := len(c.Text)
	for _, p := range c.Paths {
		if err := validatePath(p); err != nil {
			return err
		}
		bytes += len(p)
	}
	for _, a := range c.Artifacts {
		if a.BeforePath == "" && a.AfterPath == "" {
			return fmt.Errorf("empty artifact change")
		}
		for _, p := range []string{a.BeforePath, a.AfterPath} {
			if p != "" {
				if err := validatePath(p); err != nil {
					return err
				}
				bytes += len(p)
			}
		}
	}
	if bytes > 2*MaxTextBytes {
		return fmt.Errorf("guardrail context exceeds combined limits")
	}
	return nil
}
func contextPaths(c Context) []string {
	out := append([]string(nil), c.Paths...)
	for _, a := range c.Artifacts {
		if a.BeforePath != "" {
			out = append(out, a.BeforePath)
		}
		if a.AfterPath != "" {
			out = append(out, a.AfterPath)
		}
	}
	return out
}
func protected(p string) bool {
	p = strings.ToLower(p)
	for _, prefix := range []string{"internal/routineguard", "internal/routineactions", "internal/recurrences", "internal/agentd", "internal/auth", "internal/authz", "internal/db/migrations", "api/areas/recurrences.yaml", "scripts/ci", "scripts/ci-static.mjs", "scripts/check-migrations.mjs", "scripts/check-attached-policy-mutations.py", "claudeassets", "internal/recurrences/guardrails.go", "scripts/test-tiers", "scripts/audit", ".github/workflows", ".aeon", ".inspr"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "agents.md" || segment == "claude.md" || strings.Contains(segment, "guardrail") || strings.Contains(segment, "gate") || strings.Contains(segment, "guard") || segment == "rules" || strings.HasPrefix(segment, "rules.") {
			return true
		}
	}
	return false
}
func matches(r Rule, c Context, paths []string) bool {
	switch r.Method {
	case "words":
		text := strings.ToLower(c.Text + "\n" + strings.Join(paths, "\n"))
		for _, value := range r.Values {
			if strings.Contains(text, strings.ToLower(value)) {
				return true
			}
		}
	case "paths":
		for _, p := range paths {
			for _, prefix := range r.Values {
				if strings.EqualFold(p, prefix) || strings.HasPrefix(strings.ToLower(p), strings.ToLower(prefix)+"/") {
					return true
				}
			}
		}
	case "size":
		return c.PayloadBytes > r.Limit || int64(len(c.Text)) > r.Limit
	case "template":
		switch r.Template {
		case "protected_paths_v1":
			if c.ChangesGuardrails {
				return true
			}
			for _, p := range paths {
				if protected(p) {
					return true
				}
			}
		case "catalogue_v1":
			switch c.Action {
			case "work.create", "work.update", "knowledge.write", "pr.open", "pipeline.request", "budget.spend", "artifact.validate":
				return false
			}
			return true
		case "harmful_intent_v1":
			text := strings.ToLower(c.Text)
			for _, phrase := range []string{"break into someone", "steal credentials", "dox this person", "promote racial hatred", "zugangsdaten stehlen", "rassenhass verbreiten"} {
				if strings.Contains(text, phrase) {
					return true
				}
			}
		case "different_family_v1":
			return true
		}
	}
	return false
}

// Evaluate rebuilds effective policy from validated sources, bounds the entire
// input before hashing/matching, runs all applicable rules and records no raw
// payload or path in its reasons. It never trusts supplied Policy metadata.
func Evaluate(sources []Source, c Context) (Decision, error) {
	d := Decision{Result: Block, DeterministicResult: Block, Findings: []Finding{}}
	if err := ValidateContext(c); err != nil {
		return d, err
	}
	p, err := Resolve(sources)
	if err != nil {
		return d, err
	}
	d = Decision{Checkpoint: c.Checkpoint, Result: Allow, DeterministicResult: Allow, PolicyDigest: p.Digest, ContextDigest: Digest(c), HardVersion: HardVersion, PresetVersion: PresetVersion, Findings: []Finding{}}
	paths := contextPaths(c)
	for _, e := range p.Rules {
		r := e.Rule
		if e.Superseded || r.Checkpoint != "both" && r.Checkpoint != c.Checkpoint {
			continue
		}
		result, reason := Allow, "rule did not match"
		if matches(r, c, paths) {
			result, reason = r.Result, "deterministic rule matched"
		}
		if e.Level == "hard" && r.Template == "different_family_v1" {
			reason = "verified different-family evaluation is required before execution"
			d.RequiredEvaluation = &EvaluationRequirement{RuleID: r.ID, Version: HardVersion, PolicyDigest: p.Digest, ContextDigest: d.ContextDigest, DifferentFamily: true}
		} else if rank(result) > rank(d.DeterministicResult) {
			d.DeterministicResult = result
		}
		if rank(result) > rank(d.Result) {
			d.Result = result
		}
		d.Findings = append(d.Findings, Finding{RuleID: r.ID, Version: e.Version, Level: e.Level, Scope: e.Scope, Result: result, Reason: reason})
	}
	return d, nil
}
