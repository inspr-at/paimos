// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"path"
	"strings"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

const AdmissionContext = "aeon/admission"

type AdmissionHold struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}
type AdmissionPolicy struct {
	Revision          int64           `json:"revision"`
	BlockOnNightlyRed bool            `json:"block_on_nightly_red"`
	NightlyState      string          `json:"nightly_state"`
	Holds             []AdmissionHold `json:"holds"`
}
type AdmissionControl struct {
	Revision int64                 `json:"revision"`
	Binding  reviewgate.RunBinding `json:"binding"`
	Holds    []AdmissionHold       `json:"holds"`
	Fix      bool                  `json:"fix"`
}

// AdmissionApproval is populated exclusively from a submitted P1 run. It is
// never an HTTP request body. Route and family identity come from that run.
type AdmissionApproval struct {
	Binding        reviewgate.RunBinding `json:"binding"`
	Revision       int64                 `json:"binding_revision"`
	RunID          string                `json:"run_id"`
	Kind           string                `json:"kind"`
	AuthorFamily   string                `json:"author_family"`
	ReviewerFamily string                `json:"reviewer_family"`
	Harness        string                `json:"harness"`
	Model          string                `json:"model"`
	Effort         string                `json:"reasoning_effort"`
	Verdict        string                `json:"verdict"`
	VisualImpact   string                `json:"visual_impact"`
	Reason         string                `json:"reason"`
}

// AdmissionDiff is server-observed evidence for merge-base(target)..head.
// Missing/truncated lists cannot prove a visual exemption.
type AdmissionDiff struct {
	MergeBase string   `json:"merge_base"`
	Paths     []string `json:"paths"`
	Complete  bool     `json:"complete"`
}
type AdmissionInput struct {
	Binding        reviewgate.RunBinding
	Revision       int64
	Open           bool
	CIGreen        bool
	ScopeVerified  bool
	Review, Visual *AdmissionApproval
	Diff           AdmissionDiff
	Holds          []AdmissionHold
	Policy         AdmissionPolicy
	Fix            bool
}
type AdmissionDecision struct {
	Admissible     bool   `json:"admissible"`
	Reason         string `json:"reason"`
	HoldReason     string `json:"hold_reason"`
	VisualRequired bool   `json:"visual_required"`
	VisualReason   string `json:"visual_reason"`
}

// EvaluateAdmission is pure. Its facts must come from the server's current
// delivery and authenticated approval store, never caller-supplied assertions.
// Hold removal resumes the same approved head; it does not quarantine code.
func EvaluateAdmission(in AdmissionInput) AdmissionDecision {
	out := AdmissionDecision{Reason: "binding_invalid", VisualRequired: true, VisualReason: "review_missing"}
	deny := func(reason string) AdmissionDecision { out.Reason = reason; return out }
	if !in.Binding.Valid() || in.Revision < 1 {
		return out
	}
	if in.Binding.Target != "main" {
		return deny("target_not_main")
	}
	if !in.Open {
		return deny("pull_not_open")
	}
	// A deterministic hold has precedence over missing CI or approvals.
	for _, holds := range [][]AdmissionHold{in.Policy.Holds, in.Holds} {
		if len(holds) > 0 {
			out.HoldReason = holds[0].Reason
			return deny("hold_" + holds[0].Kind)
		}
	}
	if in.Policy.BlockOnNightlyRed {
		if in.Policy.NightlyState != "green" && in.Policy.NightlyState != "red" {
			return deny("nightly_unknown")
		}
		if in.Policy.NightlyState == "red" && !in.Fix {
			out.HoldReason = "Nightly is red; only designated fixes are admitted"
			return deny("nightly_red")
		}
	}
	if !in.ScopeVerified {
		return deny("scope_unverified")
	}
	if !in.CIGreen {
		return deny("ci_not_green")
	}
	matches := func(a *AdmissionApproval, kind string) bool {
		return a != nil && a.RunID != "" && a.Kind == kind && a.Binding == in.Binding && a.Revision == in.Revision
	}
	if !matches(in.Review, "review") {
		return deny("review_missing_or_stale")
	}
	r := in.Review
	if !reviewgate.ValidFamily(r.AuthorFamily) || !reviewgate.ValidFamily(r.ReviewerFamily) || r.AuthorFamily == r.ReviewerFamily || r.Model == "" || r.Harness == "" {
		return deny("review_not_cross_family")
	}
	if r.Verdict != "ok" {
		return deny("review_changes_requested")
	}
	out.VisualRequired, out.VisualReason = VisualReviewRequired(r.VisualImpact, r.Reason, in.Diff)
	if !out.VisualRequired {
		out.Admissible = true
		return deny("admissible")
	}
	if !matches(in.Visual, "visual") {
		return deny("visual_missing_or_stale")
	}
	if in.Visual.Verdict != "ok" {
		return deny("visual_changes_requested")
	}
	out.Admissible = true
	return deny("admissible")
}

// VisualReviewRequired deliberately differs from browser-test selection.
// An unchanged OpenAPI contract and arbitrary backend-only changes prove
// nothing about displayed values, permissions, ordering or visibility.
func VisualReviewRequired(impact, reason string, diff AdmissionDiff) (bool, string) {
	if impact != "no" || strings.TrimSpace(reason) == "" {
		return true, "reviewer_visual_impact"
	}
	if !diff.Complete || len(diff.Paths) == 0 || len(diff.Paths) > 2000 {
		return true, "diff_unproven"
	}
	for _, file := range diff.Paths {
		if !validAdmissionPath(file) {
			return true, "diff_unproven"
		}
		if !visualExemptPath(file) {
			return true, "presentation_not_exempt"
		}
	}
	return false, "proven_no_presentation_change"
}
func validAdmissionPath(file string) bool {
	return len(file) > 0 && len(file) <= 1024 && utf8.ValidString(file) && !strings.ContainsAny(file, "\\\x00\r\n\t") && !strings.HasPrefix(file, "/") && path.Clean(file) == file && file != ".." && !strings.HasPrefix(file, "../")
}
func visualExemptPath(file string) bool {
	if strings.HasPrefix(file, "docs/") || file == "README.md" || file == "LICENSE" || file == "AGENTS.md" {
		return true
	}
	if strings.HasSuffix(file, "_test.go") || strings.HasPrefix(file, "web/tests/") || strings.HasPrefix(file, "web/e2e/") {
		return true
	}
	if strings.HasPrefix(file, ".github/workflows/") || strings.HasPrefix(file, "scripts/test-tiers/") || strings.HasPrefix(file, "scripts/ci/") {
		return true
	}
	// Explicit reviewed allowlist. Never widen this to internal/** or all Go.
	switch file {
	case "internal/db/pool.go", "internal/db/acquire_metrics.go", "internal/db/read_timeout.go",
		"scripts/playwright-safe.mjs", "scripts/playwright-lock.mjs", "scripts/playwright-processes.mjs", "scripts/playwright-global-setup.mjs":
		return true
	}
	return false
}
func validAdmissionHolds(holds []AdmissionHold) bool {
	if len(holds) > 3 {
		return false
	}
	seen := map[string]bool{}
	for _, h := range holds {
		if h.Kind != "release_cut" && h.Kind != "wave" && h.Kind != "manual" || seen[h.Kind] || len(h.Reason) > 1000 || strings.TrimSpace(h.Reason) == "" || !utf8.ValidString(h.Reason) || strings.ContainsRune(h.Reason, '\x00') {
			return false
		}
		seen[h.Kind] = true
	}
	return true
}
