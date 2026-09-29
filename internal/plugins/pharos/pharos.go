// SPDX-License-Identifier: AGPL-3.0-only

// Package pharos is the compiled Deploy plugin. It decides whether a reviewed
// artifact may be deployed or verified. It has no database, environment, or
// HTTP client; the host consumes a launch admission only when Request says so.
package pharos

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/plugins/fence"
)

const (
	// ID is the registry id.
	ID = "pharos"
	// Version is the compiled manifest version.
	Version = "1"
	// Owner is the accountable first-party owner.
	Owner = "pharos"
	// OperationDeploy changes a host after one launch admission is consumed.
	OperationDeploy = "deploy"
	// OperationVerify reports verification of the reviewed artifact.
	OperationVerify = "verify"
	// Stage is the journey stage these operations belong to.
	Stage = "deploy"
)

// Artifact is the reviewed Pharos identity. It is the deployment fact the
// contract stores, not a Janus evidence value.
type Artifact struct {
	VersionScheme        string `json:"version_scheme"`
	Version              string `json:"version"`
	ReleaseChannel       string `json:"release_channel"`
	ReleaseSequence      int64  `json:"release_sequence"`
	DigestSHA256         string `json:"digest_sha256"`
	CommitDigest         string `json:"commit_digest"`
	ManifestCoordinate   string `json:"manifest_coordinate"`
	ManifestDigestSHA256 string `json:"manifest_digest_sha256"`
}

// Admission is the one-use launch admission bound to the artifact.
type Admission struct {
	Present          bool
	Consumed         bool
	Expired          bool
	BindingMatches   bool
	AuthorityMatches bool
}

// Dependency is one sealed prerequisite check supplied by the host.
type Dependency struct {
	Kind      string
	Required  bool
	Succeeded bool
}

// Facts is everything Deploy needs. Callers cannot add authority by setting a
// field the policy does not read.
type Facts struct {
	Operation        string
	Artifact         Artifact
	Expected         Artifact
	BackupReady      bool
	Readiness        bool
	PersonApproved   bool
	EvidenceStale    bool
	Admission        Admission
	RequiresJanus    bool
	Dependencies     []Dependency
	PrerequisiteSeal string
	Workflow         string
	Environment      string
	ObservedAt       time.Time
}

// Evidence is the deployment or verification fact Pharos would report.
type Evidence struct {
	Kind        string    `json:"kind"`
	Outcome     string    `json:"outcome"`
	Artifact    Artifact  `json:"artifact"`
	Workflow    string    `json:"workflow"`
	Environment string    `json:"environment"`
	ObservedAt  time.Time `json:"observed_at"`
}

// Decision is a pure result. AdvancesRelease is always false: a plugin result
// is not a release and cannot skip a person decision.
type Decision struct {
	Proceed                bool
	Outcome                string
	Blocker                string
	EvidenceCeiling        []string
	ConsumeLaunchAdmission bool
	Evidence               *Evidence
	AdvancesRelease        bool
}

var (
	versionScheme = map[string]bool{
		"legacy": true, "inspr-calendar-v1": true, "inspr-calendar-v2": true,
	}
	digestRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	symbolRe  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
	tokenRe   = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

// Evaluate reports whether the operation is allowed. It does not consume an admission.
func Evaluate(facts Facts) (Decision, error) {
	return decide(facts, false, false)
}

// Request is Evaluate for a new attempt. An accepted deploy asks the host to
// consume exactly one launch admission before it changes a host.
func Request(facts Facts) (Decision, error) {
	return decide(facts, false, true)
}

// Apply accepts a terminal result only when the claimed outcome matches the
// evidence and the current policy. A failed or stale observation stays failed.
func Apply(facts Facts, claimed string, evidence Evidence) (Decision, error) {
	if claimed != "succeeded" && claimed != "failed" {
		return Decision{}, fmt.Errorf("invalid outcome")
	}
	blocker, err := gate(facts, true)
	if err != nil {
		return Decision{}, err
	}
	if err := validateEvidence(facts, evidence); err != nil {
		return Decision{}, err
	}
	decision := Decision{EvidenceCeiling: ceiling(facts.Operation), AdvancesRelease: false}
	matches := evidence.Outcome == "succeeded" &&
		evidence.Kind == evidenceKind(facts.Operation) &&
		artifactEqual(evidence.Artifact, facts.Expected) &&
		evidence.Workflow == facts.Workflow &&
		evidence.Environment == facts.Environment &&
		evidence.ObservedAt.Equal(facts.ObservedAt)
	if claimed == "succeeded" && blocker == "" && matches {
		decision.Proceed = true
		decision.Outcome = "succeeded"
		decision.Evidence = &evidence
		return decision, nil
	}
	if blocker == "" {
		blocker = fence.BlockerPolicyRefused
	}
	decision.Blocker = blocker
	decision.Outcome = "failed"
	return decision, nil
}

func decide(facts Facts, forApply, asRequest bool) (Decision, error) {
	blocker, err := gate(facts, forApply)
	if err != nil {
		return Decision{}, err
	}
	decision := Decision{
		Proceed:         blocker == "",
		Blocker:         blocker,
		EvidenceCeiling: ceiling(facts.Operation),
	}
	if !decision.Proceed {
		return decision, nil
	}
	decision.Evidence = &Evidence{
		Kind:        evidenceKind(facts.Operation),
		Outcome:     "succeeded",
		Artifact:    facts.Expected,
		Workflow:    facts.Workflow,
		Environment: facts.Environment,
		ObservedAt:  facts.ObservedAt,
	}
	if asRequest && facts.Operation == OperationDeploy {
		decision.ConsumeLaunchAdmission = true
	}
	return decision, nil
}

func gate(facts Facts, forApply bool) (string, error) {
	if facts.Operation != OperationDeploy && facts.Operation != OperationVerify {
		return "", fmt.Errorf("invalid operation")
	}
	if facts.ObservedAt.IsZero() {
		return "", fmt.Errorf("observed_at is required")
	}
	if !symbolRe.MatchString(facts.Workflow) || !symbolRe.MatchString(facts.Environment) {
		return "", fmt.Errorf("invalid workflow or environment")
	}
	if err := validateArtifact(facts.Artifact); err != nil {
		return "", err
	}
	if err := validateArtifact(facts.Expected); err != nil {
		return "", err
	}
	if facts.EvidenceStale {
		return fence.BlockerReporterStale, nil
	}
	if !artifactEqual(facts.Artifact, facts.Expected) || !facts.Readiness {
		return fence.BlockerPolicyRefused, nil
	}
	if facts.Operation == OperationVerify {
		return "", nil
	}
	if !facts.PersonApproved || !facts.BackupReady {
		return fence.BlockerPolicyRefused, nil
	}
	if blocker := admissionBlocker(facts.Admission, forApply); blocker != "" {
		return blocker, nil
	}
	return prerequisiteBlocker(facts), nil
}

func admissionBlocker(a Admission, consumedMustBe bool) string {
	ok := a.Present && !a.Expired && a.BindingMatches && a.AuthorityMatches && a.Consumed == consumedMustBe
	if ok {
		return ""
	}
	return fence.BlockerPolicyRefused
}

func prerequisiteBlocker(facts Facts) string {
	if !facts.RequiresJanus {
		if len(facts.Dependencies) != 0 || facts.PrerequisiteSeal != "" {
			return fence.BlockerPolicyRefused
		}
		return ""
	}
	checks := make([]fence.Check, len(facts.Dependencies))
	failed := false
	for i, dep := range facts.Dependencies {
		checks[i] = fence.Check{Kind: dep.Kind, Required: dep.Required, Succeeded: dep.Succeeded}
		if dep.Required && !dep.Succeeded {
			failed = true
		}
	}
	seal, err := fence.Seal(checks)
	if err != nil {
		return fence.BlockerDependencyPending
	}
	if facts.PrerequisiteSeal != seal {
		return fence.BlockerPolicyRefused
	}
	if failed {
		return fence.BlockerDependencyFailed
	}
	return ""
}

func validateArtifact(a Artifact) error {
	if !versionScheme[a.VersionScheme] || !versionRe.MatchString(a.Version) {
		return fmt.Errorf("invalid artifact version")
	}
	if !symbolRe.MatchString(a.ReleaseChannel) || a.ReleaseSequence < 1 {
		return fmt.Errorf("invalid artifact channel")
	}
	if !digestRe.MatchString(a.DigestSHA256) || !digestRe.MatchString(a.ManifestDigestSHA256) {
		return fmt.Errorf("invalid artifact digest")
	}
	if !tokenRe.MatchString(a.CommitDigest) || !validCoordinate(a.ManifestCoordinate) {
		return fmt.Errorf("invalid artifact identity")
	}
	return nil
}

func validCoordinate(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	if strings.Contains(s, "..") || strings.Contains(s, "://") || strings.ContainsAny(s, "\\ \t\r\n") {
		return false
	}
	for _, r := range s {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func validateEvidence(facts Facts, evidence Evidence) error {
	switch evidence.Outcome {
	case "succeeded", "failed", "satisfied", "blocked":
	default:
		return fmt.Errorf("invalid evidence outcome")
	}
	if evidence.Kind != evidenceKind(facts.Operation) {
		return fmt.Errorf("invalid evidence kind")
	}
	return validateArtifact(evidence.Artifact)
}

func artifactEqual(a, b Artifact) bool {
	return a == b
}

func evidenceKind(operation string) string {
	if operation == OperationDeploy {
		return fence.KindDeployment
	}
	return fence.KindVerification
}

func ceiling(operation string) []string {
	return []string{evidenceKind(operation)}
}
