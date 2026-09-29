// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// AnalysisPolicy is host policy. Analysis is deterministic and its token budget
// is always zero. There is deliberately no summarizer switch or model client.
type AnalysisPolicy struct {
	WindowDays     int
	MinOccurrences int
	FixRounds      int
	MaxOpenDrafts  int
	DailyDraftCap  int
}

func (p AnalysisPolicy) defaults() AnalysisPolicy {
	bounded := func(n, fallback, max int) int {
		if n < 1 {
			return fallback
		}
		if n > max {
			return max
		}
		return n
	}
	p.WindowDays = bounded(p.WindowDays, 14, 90)
	p.MinOccurrences = bounded(p.MinOccurrences, 3, 100)
	p.FixRounds = bounded(p.FixRounds, 2, 99)
	p.MaxOpenDrafts = bounded(p.MaxOpenDrafts, 5, 100)
	p.DailyDraftCap = bounded(p.DailyDraftCap, 5, 100)
	return p
}

type analysisEvidence struct {
	ID        string `json:"id"`
	TicketID  string `json:"ticket_id"`
	TicketKey string `json:"ticket_key"`
	Href      string `json:"href"`
	Kind      string `json:"kind"`
}

type analysisMetric struct {
	Name         string    `json:"name"`
	RulesVersion string    `json:"rules_version"`
	Samples      int       `json:"samples"`
	Value        float64   `json:"value"`
	From         time.Time `json:"from"`
	Until        time.Time `json:"until"`
}

type finding struct {
	ID           string             `json:"id"`
	Pattern      string             `json:"pattern"`
	Title        string             `json:"title"`
	Count        int                `json:"count"`
	RulesVersion string             `json:"rules_version"`
	Harness      string             `json:"harness"`
	TicketKind   string             `json:"ticket_kind"`
	Status       string             `json:"status"`
	Reason       string             `json:"reason,omitempty"`
	ProposalID   string             `json:"proposal_id,omitempty"`
	PRURL        string             `json:"pr_url,omitempty"`
	RuleLabel    string             `json:"rule_label,omitempty"`
	Evidence     []analysisEvidence `json:"evidence"`
	Before       analysisMetric     `json:"before"`
	After        *analysisMetric    `json:"after,omitempty"`
	Delta        *float64           `json:"delta,omitempty"`
	CreatedAt    time.Time          `json:"created_at"`
}

// Only hashes and identifiers supplement the public view. Original summaries,
// comments, private quotations and candidate instruction text are never saved.
type findingData struct {
	finding
	SourceID     string `json:"source_id,omitempty"`
	Path         string `json:"path,omitempty"`
	RuleKey      string `json:"rule_key,omitempty"`
	RuleSHA      string `json:"rule_sha,omitempty"`
	AfterFileSHA string `json:"after_file_sha,omitempty"`
}

type analysisSample struct {
	analysisEvidence
	Version, Harness, TicketKind string
	Summary, Result              string
	Round                        int
	Elapsed                      float64
	At                           time.Time
	// File hashes belong to the latest instruction report at the event time.
	FileHashes []string
}

func hashText(s string) string { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }

// Classes are an intentionally small, explainable vocabulary. Unknown prose
// does not become a made-up class or an instruction generated from user text.
func findingClass(s string) string {
	words := " " + strings.Join(proposalWords(s), " ") + " "
	for _, c := range []struct {
		name  string
		words []string
	}{
		{"security", []string{"secret", "secrets", "credential", "credentials", "permission", "permissions", "tenant", "isolation"}},
		{"validation", []string{"test", "tests", "testing", "validation", "coverage", "regression", "ci"}},
		{"scope", []string{"scope", "ownership", "unrelated", "contract", "contracts"}},
	} {
		for _, word := range c.words {
			if strings.Contains(words, " "+word+" ") {
				return c.name
			}
		}
	}
	return ""
}

func patternFor(s analysisSample, p AnalysisPolicy) string {
	switch s.Kind {
	case "review_verdict":
		if s.Result == "changes" {
			if c := findingClass(s.Summary); c != "" {
				return "gate:" + c
			}
		}
	case "fix_round":
		if s.Round > p.FixRounds {
			return "fix_rounds"
		}
	case "learning":
		if c := findingClass(s.Summary); c != "" {
			return "learning:" + c
		}
	case "vote":
		return "exception_votes"
	case "ci_result":
		if s.Result == "fail" {
			return "ci_failures"
		}
	case "revert":
		return "reverts"
	}
	return ""
}

func patternTitle(pattern string) string {
	switch pattern {
	case "fix_rounds":
		return "Repeated fix rounds"
	case "exception_votes":
		return "Repeated requests for rework"
	case "ci_failures":
		return "Recurring CI failures"
	case "reverts":
		return "Repeated reverts"
	}
	parts := strings.SplitN(pattern, ":", 2)
	if len(parts) == 2 {
		if parts[0] == "gate" {
			return "Recurring " + parts[1] + " findings"
		}
		return "Repeated " + parts[1] + " pitfalls"
	}
	return "Outcome finding"
}

func detectFindings(samples []analysisSample, p AnalysisPolicy, from, until time.Time) []finding {
	p = p.defaults()
	groups := map[string][]analysisSample{}
	for _, s := range samples {
		// Unattributed work remains unassigned, never pooled into a guessed version.
		if s.Version == "" || s.Harness == "" || s.TicketKind == "" || s.At.Before(from) || !s.At.Before(until) {
			continue
		}
		pattern := patternFor(s, p)
		if pattern != "" {
			key := strings.Join([]string{pattern, s.Version, s.Harness, s.TicketKind}, "\x00")
			groups[key] = append(groups[key], s)
		}
	}
	out := []finding{}
	for key, hits := range groups {
		parts := strings.Split(key, "\x00")
		// Multiple rounds on one ticket and multiple voters on one session are not
		// independent recurrences. A finding needs N distinct affected tickets.
		seen := map[string]bool{}
		evidence := []analysisEvidence{}
		sort.Slice(hits, func(i, j int) bool { return hits[i].ID < hits[j].ID })
		for _, s := range hits {
			if !seen[s.TicketID] {
				seen[s.TicketID] = true
				if len(evidence) < 20 {
					evidence = append(evidence, s.analysisEvidence)
				}
			}
		}
		if len(seen) < p.MinOccurrences {
			continue
		}
		f := finding{Pattern: parts[0], Title: patternTitle(parts[0]), Count: len(seen), RulesVersion: parts[1], Harness: parts[2], TicketKind: parts[3], Evidence: evidence, Status: "pending"}
		f.Before = measure(samples, f, from, until)
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return findingFingerprint(out[i]) < findingFingerprint(out[j])
	})
	return out
}

func findingFingerprint(f finding) string {
	return hashText(strings.Join([]string{f.Pattern, f.RulesVersion, f.Harness, f.TicketKind}, "\x00"))
}

// Rates use all eligible observations, including successful reviews and CI.
// Fix rounds and completion time use one maximum per ticket. No observations
// means Samples=0, never a fabricated improvement from missing data.
func measure(samples []analysisSample, f finding, from, until time.Time) analysisMetric {
	m := analysisMetric{Name: f.Pattern, RulesVersion: f.RulesVersion, From: from, Until: until}
	values := map[string]float64{}
	for _, s := range samples {
		if s.Version != f.RulesVersion || s.Harness != f.Harness || s.TicketKind != f.TicketKind || s.At.Before(from) || !s.At.Before(until) {
			continue
		}
		key := s.ID
		value := float64(0)
		eligible := false
		switch {
		case strings.HasPrefix(f.Pattern, "gate:"):
			eligible = s.Kind == "review_verdict"
			if s.Result == "changes" && "gate:"+findingClass(s.Summary) == f.Pattern {
				value = 1
			}
		case strings.HasPrefix(f.Pattern, "learning:"):
			eligible = s.Kind == "learning"
			key = s.TicketID
			if "learning:"+findingClass(s.Summary) == f.Pattern {
				value = 1
			}
		case f.Pattern == "fix_rounds":
			eligible = s.Kind == "fix_round"
			key = s.TicketID
			value = float64(s.Round)
		case f.Pattern == "ci_failures":
			eligible = s.Kind == "ci_result"
			if s.Result == "fail" {
				value = 1
			}
		case f.Pattern == "reverts":
			eligible = s.Kind == "revert" || s.Kind == "ticket_done"
			key = s.TicketID
			if s.Kind == "revert" {
				value = 1
			}
		case f.Pattern == "exception_votes":
			eligible = s.Kind == "vote" || s.Kind == "ticket_done"
			key = s.TicketID
			if s.Kind == "vote" {
				value = 1
			}
		case f.Pattern == "time_to_done":
			eligible = s.Kind == "ticket_done" && s.Elapsed >= 0
			key = s.TicketID
			value = s.Elapsed
		}
		if eligible {
			old, ok := values[key]
			if !ok || value > old {
				values[key] = value
			}
		}
	}
	for _, value := range values {
		m.Value += value
	}
	m.Samples = len(values)
	if m.Samples > 0 {
		m.Value /= float64(m.Samples)
	}
	return m
}

func patternAdvice(pattern string) (class, sentence string) {
	switch pattern {
	case "fix_rounds":
		return "validation", "After a failed review, reproduce the finding and verify the complete fix before requesting another review."
	case "exception_votes":
		return "validation", "Check the delivered behavior against the acceptance criteria before reporting completion."
	case "ci_failures":
		return "validation", "Run the checks that cover the changed behavior before handing the change to CI."
	case "reverts":
		return "validation", "Verify the rollback path and the changed behavior before delivering the change."
	}
	parts := strings.SplitN(pattern, ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	switch parts[1] {
	case "security":
		return "security", "Verify tenant isolation and permission boundaries for the changed behavior before delivery."
	case "scope":
		return "scope", "Check the final diff against the authorized scope and preserve existing consumer contracts."
	case "validation":
		return "validation", "Reproduce the failure and verify the regression case before reporting the change as complete."
	}
	return "", ""
}

func proposalExplanation(f finding) string {
	// Counts and a fixed vocabulary only: private evidence never becomes a PR
	// quotation, ticket identifier, tenant URL, title, harness or version string.
	return fmt.Sprintf("%s across %d tickets. Baseline %s: %.4f (%d observations). Intended direction: lower. Detailed ticket and event evidence is retained in Aeon with this proposal. Compare only matching instruction provenance, harness and ticket kind; this is not a causal estimate.", f.Title, f.Count, f.Pattern, f.Before.Value, f.Before.Samples)
}
