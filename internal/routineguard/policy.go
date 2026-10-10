// SPDX-License-Identifier: AGPL-3.0-only
// Package routineguard provides the deterministic routine policy boundary. It
// neither launches models nor executes administrator-provided shell commands.
package routineguard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

const (
	HardVersion    = "routine-hard-v1"
	PresetVersion  = "routine-recommended-v1"
	MaxRules       = 64
	MaxSources     = 3
	MaxPaths       = 256
	MaxPathBytes   = 1024
	MaxTextBytes   = 131072
	MaxPolicyBytes = 131072
)

type Outcome string

const (
	Allow       Outcome = "allow"
	NeedsPerson Outcome = "needs_person"
	Block       Outcome = "block"
)

func rank(o Outcome) int {
	switch o {
	case Allow:
		return 0
	case NeedsPerson:
		return 1
	case Block:
		return 2
	}
	return -1
}

type Scope struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type Rule struct {
	ID         string   `json:"id"`
	Checkpoint string   `json:"checkpoint"`
	Method     string   `json:"method"`
	Result     Outcome  `json:"result"`
	Values     []string `json:"values,omitempty"`
	Limit      int64    `json:"limit,omitempty"`
	Template   string   `json:"template,omitempty"`
	ScriptHash string   `json:"script_hash,omitempty"`
}

// Loosening is server-created evidence, never input supplied by a routine. It
// binds the person, reason, replaced policy and exact replacement rule. A later
// inherited policy change invalidates it rather than silently widening access.
type Loosening struct {
	RuleID            string `json:"rule_id"`
	ActorID           string `json:"actor_id"`
	Reason            string `json:"reason"`
	ExpectedRevision  int64  `json:"expected_revision"`
	InheritedDigest   string `json:"inherited_digest"`
	PreviousDigest    string `json:"previous_digest"`
	ReplacementDigest string `json:"replacement_digest"`
}
type Source struct {
	Scope      Scope       `json:"scope"`
	Revision   int64       `json:"revision"`
	Rules      []Rule      `json:"rules"`
	Loosenings []Loosening `json:"loosenings"`
}
type EffectiveRule struct {
	Rule       Rule   `json:"rule"`
	Level      string `json:"level"`
	Scope      Scope  `json:"scope"`
	Version    string `json:"version"`
	Superseded bool   `json:"superseded,omitempty"`
}
type Policy struct {
	HardVersion   string          `json:"hard_version"`
	PresetVersion string          `json:"preset_version"`
	Digest        string          `json:"digest"`
	Sources       []Source        `json:"sources"`
	Rules         []EffectiveRule `json:"rules"`
}

// Actor is derived from current authorization under the final write fence.
// It is intentionally absent from the policy HTTP request schema.
type Actor struct {
	ID                    string
	Person, Administrator bool
	Reason                string
}

var ruleID = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,79}$`)
var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func Digest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Template hashes identify fixed compiled predicates, not executable paths.
// No process, shell, filesystem read or network request happens during checks.
func TemplateHash(name string) string {
	switch name {
	case "protected_paths_v1", "catalogue_v1", "harmful_intent_v1", "different_family_v1":
		return Digest(struct{ Version, Template string }{HardVersion, name})
	}
	return ""
}
func hardRules() []Rule {
	out := []Rule{}
	for _, name := range []string{"protected_paths_v1", "catalogue_v1", "harmful_intent_v1", "different_family_v1"} {
		checkpoint, result := "both", Block
		if name == "catalogue_v1" {
			checkpoint = "action"
		}
		if name == "different_family_v1" {
			checkpoint, result = "action", NeedsPerson
		}
		out = append(out, Rule{ID: "hard." + name, Checkpoint: checkpoint, Method: "template", Template: name, ScriptHash: TemplateHash(name), Result: result})
	}
	return out
}
func recommendedRules() []Rule {
	return []Rule{{ID: "recommended.sensitive", Checkpoint: "both", Method: "words", Values: []string{"password", "credential", "secret", "passwort", "zugangsdaten"}, Result: NeedsPerson},
		{ID: "recommended.security_paths", Checkpoint: "action", Method: "paths", Values: []string{"internal/auth", "internal/authz", "internal/db/migrations"}, Result: NeedsPerson}}
}
func scopeRank(kind string) int {
	switch kind {
	case "tenant":
		return 0
	case "project":
		return 1
	case "user":
		return 2
	}
	return -1
}
func ValidateRules(rules []Rule) error {
	if len(rules) > MaxRules {
		return fmt.Errorf("too many guardrail rules")
	}
	seen := map[string]bool{}
	total := 0
	for _, r := range rules {
		if len(r.ID) > 80 || !ruleID.MatchString(r.ID) || strings.HasPrefix(r.ID, "hard.") || seen[r.ID] {
			return fmt.Errorf("invalid, duplicate or hard rule id")
		}
		seen[r.ID] = true
		if rank(r.Result) < 0 || (r.Checkpoint != "save" && r.Checkpoint != "action" && r.Checkpoint != "both") || len(r.Values) > 32 {
			return fmt.Errorf("invalid guardrail outcome, checkpoint or values")
		}
		for _, value := range r.Values {
			total += len(value)
			if value == "" || len(value) > MaxPathBytes {
				return fmt.Errorf("guardrail value exceeds limits")
			}
		}
		if total > MaxPolicyBytes {
			return fmt.Errorf("guardrail policy exceeds limits")
		}
		switch r.Method {
		case "words", "paths":
			if len(r.Values) == 0 || r.Limit != 0 || r.Template != "" || r.ScriptHash != "" {
				return fmt.Errorf("invalid deterministic rule arguments")
			}
			if r.Method == "paths" {
				for _, v := range r.Values {
					if err := validatePath(v); err != nil {
						return err
					}
				}
			}
		case "size":
			if r.Limit < 1 || r.Limit > MaxTextBytes || len(r.Values) != 0 || r.Template != "" || r.ScriptHash != "" {
				return fmt.Errorf("invalid size rule")
			}
		case "template":
			if TemplateHash(r.Template) == "" || r.ScriptHash != TemplateHash(r.Template) || r.Limit != 0 || len(r.Values) != 0 {
				return fmt.Errorf("unregistered template or replaced script hash")
			}
		default:
			return fmt.Errorf("unknown guardrail method")
		}
	}
	return nil
}
func tightens(next, before Rule) bool {
	// Changing the predicate or checkpoint requires administrator evidence even
	// if a caller describes the edit as tightening; no implication is guessed.
	n, b := next, before
	n.Result, b.Result = Allow, Allow
	if !reflect.DeepEqual(n, b) {
		return false
	}
	return rank(next.Result) >= rank(before.Result)
}
func validEvidence(e Loosening, s Source, inherited string, r Rule) bool {
	return e.RuleID == r.ID && uuid.MatchString(e.ActorID) && strings.TrimSpace(e.Reason) != "" && len(e.Reason) <= 2048 && e.ExpectedRevision == s.Revision-1 && e.InheritedDigest == inherited && e.ReplacementDigest == Digest(r) && len(e.PreviousDigest) == 64
}

// Resolve always installs the compiled hard floor and preset. A caller cannot
// replace either by supplying a serialized Policy. Scoped sources are checked
// in order and every unsuperseded applicable rule is retained for evaluation.
func Resolve(sources []Source) (Policy, error) {
	if sources == nil {
		sources = []Source{}
	}
	p := Policy{HardVersion: HardVersion, PresetVersion: PresetVersion, Sources: sources, Rules: []EffectiveRule{}}
	if len(sources) > MaxSources {
		return p, fmt.Errorf("too many guardrail sources")
	}
	for _, r := range hardRules() {
		p.Rules = append(p.Rules, EffectiveRule{Rule: r, Level: "hard", Scope: Scope{Kind: "installation"}, Version: HardVersion})
	}
	for _, r := range recommendedRules() {
		p.Rules = append(p.Rules, EffectiveRule{Rule: r, Level: "recommended", Scope: Scope{Kind: "installation"}, Version: PresetVersion})
	}
	last := -1
	for _, s := range sources {
		order := scopeRank(s.Scope.Kind)
		if order <= last || len(s.Scope.ID) != 36 || !uuid.MatchString(s.Scope.ID) || s.Revision < 1 || len(s.Loosenings) > MaxRules {
			return p, fmt.Errorf("invalid guardrail scope or revision")
		}
		last = order
		if err := ValidateRules(s.Rules); err != nil {
			return p, err
		}
		// Bound evidence before JSON hashing; imported persisted records are inputs too.
		for _, e := range s.Loosenings {
			if len(e.Reason) > 2048 || len(e.RuleID) > 80 || len(e.ActorID) > 36 || len(e.InheritedDigest) > 64 || len(e.PreviousDigest) > 64 || len(e.ReplacementDigest) > 64 {
				return p, fmt.Errorf("override evidence exceeds limits")
			}
		}
		inherited := Digest(p.Rules)
		for _, r := range s.Rules {
			weaken := false
			for _, old := range p.Rules {
				if old.Rule.ID == r.ID && !old.Superseded && !tightens(r, old.Rule) {
					weaken = true
				}
			}
			if weaken {
				approved := false
				for _, e := range s.Loosenings {
					if validEvidence(e, s, inherited, r) {
						approved = true
					}
				}
				if !approved {
					return p, fmt.Errorf("unapproved or stale guardrail loosening")
				}
				for i := range p.Rules {
					if p.Rules[i].Rule.ID == r.ID {
						if p.Rules[i].Level == "hard" {
							return p, fmt.Errorf("hard rules cannot be overridden")
						}
						p.Rules[i].Superseded = true
					}
				}
			}
			p.Rules = append(p.Rules, EffectiveRule{Rule: r, Level: "custom", Scope: s.Scope, Version: fmt.Sprint(s.Revision)})
		}
	}
	raw, err := json.Marshal(p.Rules)
	if err != nil || len(raw) > 4*MaxPolicyBytes {
		return p, fmt.Errorf("effective policy exceeds limits")
	}
	p.Digest = Digest(p.Rules)
	return p, nil
}

// Change replaces one scoped revision. Only a verified person may edit rules;
// removals, predicate changes and weaker outcomes require an administrator and
// a reason. The HTTP layer persists the returned evidence unchanged.
func Change(inherited []Source, before Source, rules []Rule, expected int64, actor Actor) (Source, error) {
	if rules == nil {
		rules = []Rule{}
	}
	out := Source{Scope: before.Scope, Revision: expected + 1, Rules: rules, Loosenings: []Loosening{}}
	if !actor.Person || !uuid.MatchString(actor.ID) {
		return out, fmt.Errorf("only a person may edit guardrails")
	}
	if expected != before.Revision || expected < 0 || expected == 9223372036854775807 {
		return out, fmt.Errorf("guardrail revision changed")
	}
	if scopeRank(before.Scope.Kind) < 0 || !uuid.MatchString(before.Scope.ID) {
		return out, fmt.Errorf("invalid guardrail scope")
	}
	if err := ValidateRules(rules); err != nil {
		return out, err
	}
	parent, err := Resolve(inherited)
	if err != nil {
		return out, err
	}
	old := map[string]Rule{}
	for _, r := range before.Rules {
		old[r.ID] = r
	}
	desired := map[string]Rule{}
	for _, r := range rules {
		desired[r.ID] = r
	}
	needsReason := false
	for id, r := range old {
		n, ok := desired[id]
		if !ok || !tightens(n, r) {
			needsReason = true
		}
	}
	for _, r := range rules {
		weaker := false
		if oldRule, ok := old[r.ID]; ok && !tightens(r, oldRule) {
			weaker = true
		}
		for _, prior := range parent.Rules {
			if !prior.Superseded && prior.Rule.ID == r.ID && !tightens(r, prior.Rule) {
				weaker = true
			}
		}
		if weaker {
			needsReason = true
			out.Loosenings = append(out.Loosenings, Loosening{RuleID: r.ID, ActorID: actor.ID, Reason: strings.TrimSpace(actor.Reason), ExpectedRevision: expected, InheritedDigest: Digest(parent.Rules), PreviousDigest: Digest(before), ReplacementDigest: Digest(r)})
		}
	}
	if needsReason && (!actor.Administrator || strings.TrimSpace(actor.Reason) == "" || len(actor.Reason) > 2048) {
		return out, fmt.Errorf("loosening requires a current administrator and explicit reason")
	}
	// Carry still-valid inherited overrides forward with new revision evidence.
	// It is itself a loosening: an ordinary person cannot perpetuate the exception.
	all := append(append([]Source(nil), inherited...), out)
	if _, err := Resolve(all); err != nil {
		return out, err
	}
	return out, nil
}
