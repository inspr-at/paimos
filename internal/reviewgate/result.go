// SPDX-License-Identifier: AGPL-3.0-only

// Package reviewgate owns immutable review bindings and fail-closed verdicts.
// It has no dispatch, model credentials, or merge authority.
package reviewgate

import (
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxOutput = 64 << 10
const PolicyHeader = "X-Aeon-Review-Policy"
const Policy = "no_tools_v1"

type Binding struct {
	TicketSnapshot string  `json:"ticket_snapshot,omitempty"`
	TicketID       string  `json:"ticket_node_id"`
	Repository     string  `json:"repository"`
	BaseSHA        string  `json:"base_sha"`
	HeadSHA        string  `json:"head_sha"`
	AuthorRunID    *string `json:"author_run_id"`
	AuthorFamily   string  `json:"author_family"`
	ProfileID      *string `json:"reviewer_profile_id"`
	ReviewerFamily *string `json:"reviewer_family"`
	PullRequest    *int64  `json:"pull_request"`
}

type Finding struct {
	Severity string `json:"severity"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
}
type Result struct {
	Verdict  string    `json:"verdict"`
	Findings []Finding `json:"findings"`
	Reason   string    `json:"reason"`
}

var sha = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var repository = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var finding = regexp.MustCompile(`^FINDING: (critical|high|medium|low) ([^\s:]+):([1-9][0-9]*) (.+)$`)
var secretText = regexp.MustCompile(`(?:-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----|aeon_[A-Za-z0-9]{8,}_[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_-]{24,})`)

// SensitiveText is a conservative preflight for known credential forms. It
// never echoes the match; unknown forms still depend on repository hygiene.
func SensitiveText(s string) bool { return secretText.MatchString(s) }

func ValidSHA(s string) bool { return sha.MatchString(s) }
func ValidRepository(s string) bool {
	if len(s) > 200 || !repository.MatchString(s) {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}
func ValidFamily(s string) bool {
	return s == "openai" || s == "anthropic" || s == "xai" || s == "cursor" || s == "google" || s == "local"
}

// Parse accepts only the final nonempty verdict line. Earlier verdicts in
// quoted evidence cannot open the gate; findings use a strict line grammar.
func Parse(output string) Result {
	r := Result{Findings: []Finding{}}
	invalid := func(reason string) Result { r.Verdict = ""; r.Reason = reason; return r }
	if len(output) > MaxOutput || !utf8.ValidString(output) || strings.ContainsRune(output, '\x00') {
		return invalid("Review output exceeds its bound or is invalid.")
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	tail := strings.TrimSuffix(lines[len(lines)-1], "\r")
	switch tail {
	case "VERDICT: ok":
		r.Verdict = "ok"
	case "VERDICT: changes":
		r.Verdict = "changes"
	default:
		return invalid("The final line must be VERDICT: ok or VERDICT: changes.")
	}
	for _, line := range lines[:len(lines)-1] {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, "FINDING:") {
			continue
		}
		m := finding.FindStringSubmatch(line)
		if m == nil || len(r.Findings) >= 100 {
			return invalid("Review findings are malformed or exceed their bound.")
		}
		n, err := strconv.Atoi(m[3])
		if err != nil || n > 10_000_000 || len(m[2]) > 500 || path.IsAbs(m[2]) || path.Clean(m[2]) != m[2] || strings.HasPrefix(m[2], "../") || strings.Contains(m[2], "\\") || len(m[4]) > 2000 || strings.TrimSpace(m[4]) == "" {
			return invalid("A finding requires a relative file, a positive line and a bounded message.")
		}
		r.Findings = append(r.Findings, Finding{m[1], m[2], n, m[4]})
	}
	if r.Verdict == "changes" && len(r.Findings) == 0 {
		return invalid("Changes need at least one finding with file and line.")
	}
	for _, f := range r.Findings {
		if r.Verdict == "ok" && (f.Severity == "critical" || f.Severity == "high") {
			return invalid("Blocking findings contradict an ok verdict.")
		}
	}
	return r
}

// Gate never treats a completed process or an unverified model as approval.
func Gate(status, modelEvidence string, effectiveModel *string, b Binding, result Result) (bool, string) {
	if b.ReviewerFamily == nil || b.ProfileID == nil {
		return false, "No eligible reviewer from another family is available."
	}
	if !ValidFamily(*b.ReviewerFamily) || *b.ReviewerFamily == b.AuthorFamily {
		return false, "The author family cannot review itself."
	}
	if status != "completed" {
		return false, "Review " + status + "; the gate stays closed."
	}
	if modelEvidence != "vendor_reported" || effectiveModel == nil || *effectiveModel == "" {
		return false, "The reviewer model was not verified by its harness."
	}
	if result.Reason != "" {
		return false, result.Reason
	}
	if result.Verdict != "ok" {
		return false, "Changes are required before this commit range can pass review."
	}
	return true, "This commit range passed independent review."
}

// CommitRange is bounded, content-free evidence supplied by a builder daemon.
type CommitRange struct {
	Repository string `json:"repository"`
	BaseSHA    string `json:"base_sha"`
	HeadSHA    string `json:"head_sha"`
}

func (r CommitRange) Valid() bool {
	return ValidRepository(r.Repository) && ValidSHA(r.BaseSHA) && ValidSHA(r.HeadSHA) && r.BaseSHA != r.HeadSHA
}

// ModelMatches requires the requested model or a concrete resolution of a
// Claude catalog alias. A weaker/different model cannot satisfy the pin.
func ModelMatches(requested, effective string) bool {
	if requested == "" || effective == "" {
		return false
	}
	if requested == effective {
		return true
	}
	if requested == "fable" || requested == "opus" {
		return strings.HasPrefix(effective, "claude-"+requested+"-")
	}
	return false
}
