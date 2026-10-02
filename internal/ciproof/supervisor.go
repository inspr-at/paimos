// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	TaskSchema     = "aeon.ci.vm-task.v1"
	GuestSchema    = "aeon.ci.guest-result.v1"
	maxGuestOutput = 8 << 20
	maxArtifact    = 4 << 20
)

// FilePin is a raw SHA-256 pin, not a candidate-created digest or a cache key.
// Host installation is root-owned, non-writable by the QEMU UID, and no-follow.
type FilePin struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type Recipe struct {
	ObligationID   string   `json:"obligation_id"`
	Stage          string   `json:"stage"`
	Argv           []string `json:"argv"`
	Reporter       string   `json:"reporter"`
	Expected       []string `json:"expected"`
	ArtifactPaths  []string `json:"artifact_paths,omitempty"`
	OutputArtifact string   `json:"output_artifact,omitempty"`
}

// VMProfile is operator-installed alongside the immutable supervisor. Every
// stage gets a new VM. There is no candidate env, runner route or QEMU argument
// field. Images contain offline dependencies, approved harness/config/fixtures,
// and absolute tool paths; candidate npm installs never supply the test image.
type VMProfile struct {
	Schema            string   `json:"schema"`
	Infrastructure    string   `json:"infrastructure"`
	QEMU              FilePin  `json:"qemu"`
	Kernel            FilePin  `json:"kernel"`
	Initrd            FilePin  `json:"initrd"`
	RootFS            FilePin  `json:"rootfs"`
	Firmware          FilePin  `json:"firmware"`
	HarnessDigest     string   `json:"harness_digest"`
	ToolchainDigest   string   `json:"toolchain_digest"`
	EnvironmentDigest string   `json:"environment_digest"`
	SecurityEpoch     string   `json:"security_epoch"`
	UID               uint32   `json:"uid"`
	GID               uint32   `json:"gid"`
	MemoryMiB         int      `json:"memory_mib"`
	CPUs              int      `json:"cpus"`
	TimeoutSeconds    int      `json:"timeout_seconds"`
	Recipes           []Recipe `json:"recipes"`
}

func ProfileDigest(p VMProfile) string { return digest("immutable-vm-profile", p) }

func validateProfile(p VMProfile) error {
	if p.Schema != "aeon.ci.vm-profile.v1" || p.Infrastructure != "hosted-disposable" || p.UID < 1000 || p.GID == 0 || p.MemoryMiB < 128 || p.MemoryMiB > 4096 || p.CPUs < 1 || p.CPUs > 2 || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 1800 || p.SecurityEpoch == "" || len(p.SecurityEpoch) > 128 || len(p.Recipes) == 0 ||
		!digestID.MatchString(p.HarnessDigest) || !digestID.MatchString(p.ToolchainDigest) || !digestID.MatchString(p.EnvironmentDigest) {
		return fmt.Errorf("invalid immutable VM profile")
	}
	for _, f := range []FilePin{p.QEMU, p.Kernel, p.Initrd, p.RootFS, p.Firmware} {
		if !safeAbsolute(f.Path) || !digestID.MatchString(f.Digest) {
			return fmt.Errorf("absolute pinned installation files required")
		}
	}
	seen := map[string]bool{}
	for _, r := range p.Recipes {
		if r.ObligationID == "" || seen[r.ObligationID] || !validRecipe(r) {
			return fmt.Errorf("invalid or duplicate trusted recipe")
		}
		seen[r.ObligationID] = true
	}
	return nil
}

func safeAbsolute(p string) bool {
	return strings.HasPrefix(p, "/") && path.Clean(p) == p && !strings.ContainsAny(p, "\x00\n\r,")
}

func validRecipe(r Recipe) bool {
	if r.Stage != "metadata" && r.Stage != "build" && r.Stage != "test" && r.Stage != "browser" {
		return false
	}
	if r.Reporter != "command" && r.Reporter != "go-json" && r.Reporter != "harness-json" {
		return false
	}
	if len(r.Argv) == 0 || len(r.Argv) > 256 || !safeAbsolute(r.Argv[0]) || !strings.HasPrefix(r.Argv[0], "/opt/aeon/") || !sortedUnique(r.Expected) {
		return false
	}
	for _, arg := range r.Argv {
		if len(arg) > 4096 || strings.ContainsAny(arg, "\x00\r\n") {
			return false
		}
	}
	if len(r.ArtifactPaths) > 16 {
		return false
	}
	for _, name := range r.ArtifactPaths {
		if !SafeArtifactPath(name) {
			return false
		}
	}
	if r.OutputArtifact != "" && (r.Stage != "metadata" || !SafeArtifactPath(r.OutputArtifact) || path.Base(r.OutputArtifact) != r.OutputArtifact) {
		return false
	}
	// Browser tests/config/fixtures live in the approved image. Launching a
	// candidate browser collector or Node helper cannot impersonate that harness.
	if r.Stage == "browser" && r.Reporter != "harness-json" {
		return false
	}
	if r.Reporter == "go-json" {
		fresh, jsonOutput, tests := false, false, false
		for _, arg := range r.Argv[1:] {
			if arg == "-count=1" {
				fresh = true
			}
			if arg == "-json" {
				jsonOutput = true
			}
		}
		for _, id := range r.Expected {
			if strings.HasPrefix(id, "test/") {
				tests = true
			}
		}
		if !fresh || !jsonOutput || !tests {
			return false
		}
	}
	if r.Reporter == "command" && (len(r.Expected) != 1 || r.Expected[0] != "command/"+r.ObligationID) {
		return false
	}
	return true
}

// Admission binds *all* executable candidate bytes via the independent tree
// manifest, including Go init functions, JS helpers, config and lifecycle code.
// A signature over candidate-authored test names is deliberately insufficient.
// The independent admission signer is not available to the supervisor/guest.
type Admission struct {
	Schema             string `json:"schema"`
	PlanID             string `json:"plan_id"`
	CandidateCommit    string `json:"candidate_commit"`
	TreeManifestDigest string `json:"tree_manifest_digest"`
	PolicyDigest       string `json:"policy_digest"`
	ExecutorDigest     string `json:"executor_digest"`
	HarnessDigest      string `json:"harness_digest"`
	EnvironmentDigest  string `json:"environment_digest"`
	SecurityEpoch      string `json:"security_epoch"`
	ReviewTarget       string `json:"review_target"`
	ReviewRecord       string `json:"review_record"`
	ApprovedAt         string `json:"approved_at"`
	ExpiresAt          string `json:"expires_at"`
	WholeTreeAudited   bool   `json:"whole_tree_audited"`
	Signature          []byte `json:"signature"`
}

func admissionMessage(a Admission) []byte {
	a.Signature = nil
	return []byte(digest("independent-executable-admission", a))
}

func VerifyAdmission(p Plan, profile VMProfile, a Admission, key ed25519.PublicKey, now time.Time) error {
	for _, reason := range p.Reasons {
		if strings.HasPrefix(reason, "candidate-policy-change:") {
			return fmt.Errorf("candidate policy changes cannot enter the previous trust epoch")
		}
	}
	start, e1 := time.Parse(time.RFC3339Nano, a.ApprovedAt)
	end, e2 := time.Parse(time.RFC3339Nano, a.ExpiresAt)
	if a.Schema != "aeon.ci.executable-admission.v1" || len(key) != ed25519.PublicKeySize || len(a.Signature) != ed25519.SignatureSize || !ed25519.Verify(key, admissionMessage(a), a.Signature) ||
		!a.WholeTreeAudited || a.ReviewRecord == "" || len(a.ReviewRecord) > 256 || a.PlanID != p.ID || a.CandidateCommit != p.Candidate.Commit || a.TreeManifestDigest != p.Candidate.ManifestDigest || a.PolicyDigest != p.Policy.Digest ||
		a.ExecutorDigest != ProfileDigest(profile) || a.HarnessDigest != profile.HarnessDigest || a.EnvironmentDigest != p.EnvironmentDigest || a.EnvironmentDigest != profile.EnvironmentDigest || a.SecurityEpoch != profile.SecurityEpoch || a.ReviewTarget != p.Binding.CheckTarget ||
		e1 != nil || e2 != nil || now.Before(start) || !now.Before(end) || !end.After(start) || end.Sub(start) > 24*time.Hour {
		return fmt.Errorf("missing, expired or mismatched independent executable admission")
	}
	return nil
}

type VMTask struct {
	Schema          string   `json:"schema"`
	PlanID          string   `json:"plan_id"`
	ObligationID    string   `json:"obligation_id"`
	Fingerprint     string   `json:"fingerprint"`
	CandidateCommit string   `json:"candidate_commit"`
	SourceDigest    string   `json:"source_digest"`
	ExecutorDigest  string   `json:"executor_digest"`
	Stage           string   `json:"stage"`
	Argv            []string `json:"argv"`
	Reporter        string   `json:"reporter"`
	Expected        []string `json:"expected"`
	ArtifactPaths   []string `json:"artifact_paths,omitempty"`
	OutputArtifact  string   `json:"output_artifact,omitempty"`
}

func DecodeVMTask(raw []byte) (VMTask, error) {
	var task VMTask
	if err := decodeStrict(raw, &task); err != nil {
		return VMTask{}, err
	}
	r := Recipe{ObligationID: task.ObligationID, Stage: task.Stage, Argv: task.Argv, Reporter: task.Reporter, Expected: task.Expected, ArtifactPaths: task.ArtifactPaths, OutputArtifact: task.OutputArtifact}
	if task.Schema != TaskSchema || !digestID.MatchString(task.PlanID) || !digestID.MatchString(task.Fingerprint) || !digestID.MatchString(task.SourceDigest) || !digestID.MatchString(task.ExecutorDigest) || !objectID.MatchString(task.CandidateCommit) || !validRecipe(r) {
		return VMTask{}, fmt.Errorf("invalid immutable guest task")
	}
	return task, nil
}

// GuestResult is guest output, not execution provenance or a signed receipt.
// Untrusted stdout is a bounded diagnostic digest; only the approved guest
// executor emits the terminal manifest. Independent whole-tree admission is
// still necessary: provenance cannot prove honest attacker-written assertions.
type GuestResult struct {
	Schema         string            `json:"schema"`
	PlanID         string            `json:"plan_id"`
	ObligationID   string            `json:"obligation_id"`
	ExecutorDigest string            `json:"executor_digest"`
	ExitCode       int               `json:"exit_code"`
	Manifest       ExecutionManifest `json:"manifest"`
	OutputDigest   string            `json:"output_digest"`
	Artifacts      []Artifact        `json:"artifacts"`
}

type Artifact struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Data   []byte `json:"data"`
}

func SafeArtifactPath(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 256 && path.Clean(name) == name && !strings.HasPrefix(name, "/") && !strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\\x00\r\n")
}

func RawDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ValidateArtifacts never extracts tarballs, loads JS, mounts disks or accepts
// guest paths. The bounded interface returns content-addressed opaque bytes.
func ValidateArtifacts(artifacts []Artifact) error {
	total := 0
	seen := map[string]bool{}
	for _, a := range artifacts {
		total += len(a.Data)
		if total > maxArtifact || len(artifacts) > 16 || a.Name == "" || len(a.Name) > 128 || path.Base(a.Name) != a.Name || a.Name == "." || a.Name == ".." || strings.ContainsAny(a.Name, "\\\x00\r\n") || seen[a.Name] || a.Digest != RawDigest(a.Data) {
			return fmt.Errorf("invalid bounded artifact transfer")
		}
		seen[a.Name] = true
	}
	return nil
}

// Supervisor alone creates in-process authenticated observations. Serialization
// loses the private seal; E must independently sign durable proof records.
type SupervisedObservation struct {
	Receipt         Receipt    `json:"receipt"`
	Stage           string     `json:"stage"`
	AdmissionDigest string     `json:"admission_digest"`
	Artifacts       []Artifact `json:"artifacts"`
	Admission       Admission  `json:"admission"`
	seal            []byte
}

type Supervisor struct {
	repo    *Repository
	profile VMProfile
	key     ed25519.PublicKey
	sealKey []byte
	mu      sync.RWMutex
	revoked map[string]bool
}

func NewSupervisor(r *Repository, p VMProfile, key ed25519.PublicKey) (*Supervisor, error) {
	if r == nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("controller mirror and independent admission public key required")
	}
	if err := validateProfile(p); err != nil {
		return nil, err
	}
	// Freeze caller slices: subsequent candidate/metadata mutation cannot rewrite
	// recipes, tool paths, expected identities or admission authority.
	raw, _ := json.Marshal(p)
	var frozen VMProfile
	_ = json.Unmarshal(raw, &frozen)
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("supervisor randomness unavailable")
	}
	return &Supervisor{repo: r, profile: frozen, key: bytes.Clone(key), sealKey: secret, revoked: map[string]bool{}}, nil
}

// RevokeAdmission is called only by trusted ingress, never by an execution
// message. In-flight and previously observed runs remain closed after revocation.
func (s *Supervisor) RevokeAdmission(reviewRecord string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked[reviewRecord] = true
}

func (s *Supervisor) admitted(p Plan, a Admission) error {
	s.mu.RLock()
	revoked := s.revoked[a.ReviewRecord]
	s.mu.RUnlock()
	if revoked {
		return fmt.Errorf("executable admission revoked")
	}
	return VerifyAdmission(p, s.profile, a, s.key, time.Now())
}

func (s *Supervisor) task(p Plan, id, sourceDigest string) (VMTask, error) {
	var unit *Obligation
	for i := range p.Obligations {
		if p.Obligations[i].ID == id {
			unit = &p.Obligations[i]
			break
		}
	}
	if unit == nil || !digestID.MatchString(sourceDigest) || p.EnvironmentDigest != s.profile.EnvironmentDigest {
		return VMTask{}, fmt.Errorf("unknown obligation or environment")
	}
	for _, r := range s.profile.Recipes {
		if r.ObligationID != id {
			continue
		}
		if unit.Kind != "job" && r.Reporter == "command" {
			return VMTask{}, fmt.Errorf("test obligation cannot use command-only evidence")
		}
		return VMTask{Schema: TaskSchema, PlanID: p.ID, ObligationID: id, Fingerprint: unit.Fingerprint, CandidateCommit: p.Candidate.Commit, SourceDigest: sourceDigest, ExecutorDigest: ProfileDigest(s.profile), Stage: r.Stage, Argv: append([]string(nil), r.Argv...), Reporter: r.Reporter, Expected: append([]string(nil), r.Expected...), ArtifactPaths: append([]string(nil), r.ArtifactPaths...), OutputArtifact: r.OutputArtifact}, nil
	}
	return VMTask{}, fmt.Errorf("no approved executor recipe; full CI retained")
}

type ExecutionIdentity struct {
	RunID, Attempt int64
	JobID          string
}

// Run never accepts a candidate command, env, manifest, report path or host
// artifact mount. Sources come only from the controller's immutable bare Git.
// Runtime checks refuse Actions, non-Linux/non-root hosts and mutable installs.
func (s *Supervisor) Run(ctx context.Context, p Plan, id string, a Admission, identity ExecutionIdentity) (SupervisedObservation, error) {
	if identity.RunID <= 0 || identity.Attempt != 1 || identity.JobID == "" || len(identity.JobID) > 256 {
		return SupervisedObservation{}, fmt.Errorf("new complete attempt identity required")
	}
	if err := VerifyPlan(ctx, s.repo, p); err != nil {
		return SupervisedObservation{}, err
	}
	if err := s.admitted(p, a); err != nil {
		return SupervisedObservation{}, err
	}
	source, err := s.repo.sourceArchive(ctx, p.Candidate)
	if err != nil {
		return SupervisedObservation{}, err
	}
	task, err := s.task(p, id, RawDigest(source))
	if err != nil {
		return SupervisedObservation{}, err
	}
	if task.Stage == "browser" || task.Reporter == "harness-json" {
		return SupervisedObservation{}, fmt.Errorf("approved browser/application VM boundary not provisioned")
	}
	start := time.Now()
	raw, err := runVM(ctx, s.profile, task, source)
	if err != nil {
		return SupervisedObservation{}, err
	}
	result, err := decodeGuest(task, raw)
	if err != nil {
		return SupervisedObservation{}, err
	}
	if err := s.admitted(p, a); err != nil {
		return SupervisedObservation{}, err
	}
	return s.observe(p, task, a, identity, result, start, time.Now())
}

func decodeStrict(raw []byte, dst any) error {
	if len(raw) > maxGuestOutput {
		return fmt.Errorf("boundary JSON size limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := uniqueJSON(d); err != nil {
		return fmt.Errorf("invalid boundary JSON")
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing boundary JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return fmt.Errorf("invalid boundary JSON")
	}
	return nil
}

func decodeGuest(task VMTask, raw []byte) (GuestResult, error) {
	var r GuestResult
	if err := decodeStrict(raw, &r); err != nil {
		return GuestResult{}, err
	}
	if r.Schema != GuestSchema || r.PlanID != task.PlanID || r.ObligationID != task.ObligationID || r.ExecutorDigest != task.ExecutorDigest || !digestID.MatchString(r.OutputDigest) || r.ExitCode < 0 || r.ExitCode > 255 || !sortedUnique(r.Manifest.Expected) ||
		strings.Join(r.Manifest.Expected, "\x00") != strings.Join(task.Expected, "\x00") || len(r.Manifest.Skipped) != 0 || !sortedUnique(r.Manifest.Executed) || strings.Join(r.Manifest.Executed, "\x00") != strings.Join(task.Expected, "\x00") {
		return GuestResult{}, fmt.Errorf("guest completion is missing, narrowed or forged")
	}
	wantKind := "test"
	if task.Reporter == "command" {
		wantKind = "non-test"
	}
	if r.Manifest.Kind != wantKind {
		return GuestResult{}, fmt.Errorf("guest manifest kind mismatch")
	}
	if err := ValidateArtifacts(r.Artifacts); err != nil {
		return GuestResult{}, err
	}
	wantArtifacts := []string{}
	actualArtifacts := []string{}
	for _, name := range task.ArtifactPaths {
		wantArtifacts = append(wantArtifacts, path.Base(name))
	}
	if task.OutputArtifact != "" {
		wantArtifacts = append(wantArtifacts, task.OutputArtifact)
	}
	for _, artifact := range r.Artifacts {
		actualArtifacts = append(actualArtifacts, artifact.Name)
		if artifact.Name == task.OutputArtifact && artifact.Digest != r.OutputDigest {
			return GuestResult{}, fmt.Errorf("metadata output digest mismatch")
		}
	}
	sort.Strings(wantArtifacts)
	sort.Strings(actualArtifacts)
	if strings.Join(wantArtifacts, "\x00") != strings.Join(actualArtifacts, "\x00") {
		return GuestResult{}, fmt.Errorf("missing or unexpected guest artifact")
	}
	return r, nil
}

func (s *Supervisor) observe(p Plan, task VMTask, a Admission, identity ExecutionIdentity, r GuestResult, start, end time.Time) (SupervisedObservation, error) {
	result := "failure"
	if r.ExitCode == 0 {
		result = "success"
	}
	artifacts := []string{}
	for _, a := range r.Artifacts {
		artifacts = append(artifacts, a.Digest)
	}
	sort.Strings(artifacts)
	receipt := Receipt{Schema: ReceiptSchema, PlanID: p.ID, ObligationID: task.ObligationID, Fingerprint: task.Fingerprint, EnvironmentDigest: p.EnvironmentDigest, CandidateCommit: p.Candidate.Commit, CandidateTree: p.Candidate.Tree, PolicyDigest: p.Policy.Digest, WorkflowCommit: p.Policy.Commit, ExecutorDigest: task.ExecutorDigest, RunID: identity.RunID, Attempt: identity.Attempt, JobID: identity.JobID, StartedAt: start.UTC().Format(time.RFC3339Nano), CompletedAt: end.UTC().Format(time.RFC3339Nano), Result: result, Manifest: r.Manifest, ArtifactDigests: artifacts}
	if err := ValidateReceipt(p, receipt); err != nil {
		return SupervisedObservation{}, err
	}
	o := SupervisedObservation{Receipt: receipt, Stage: task.Stage, AdmissionDigest: digest("verified-admission", a), Artifacts: r.Artifacts, Admission: a}
	o.seal = s.seal(o)
	return o, nil
}

func (s *Supervisor) seal(o SupervisedObservation) []byte {
	m := hmac.New(sha256.New, s.sealKey)
	raw, _ := json.Marshal(o)
	_, _ = m.Write(raw)
	return m.Sum(nil)
}

func (s *Supervisor) Verify(p Plan, o SupervisedObservation) error {
	if !hmac.Equal(o.seal, s.seal(o)) || o.Receipt.ExecutorDigest != ProfileDigest(s.profile) || !digestID.MatchString(o.AdmissionDigest) {
		return fmt.Errorf("observation was not minted by this external supervisor")
	}
	if o.AdmissionDigest != digest("verified-admission", o.Admission) {
		return fmt.Errorf("observation admission mismatch")
	}
	if err := s.admitted(p, o.Admission); err != nil {
		return err
	}
	return ValidateReceipt(p, o.Receipt)
}
