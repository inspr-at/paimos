// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var policyPaths = []string{".github/workflows/ci.yml", "scripts/ci/go-shards.txt", "scripts/ci/go-shards-4.txt"}
var requiredContexts = []string{"e2e", "go", "migration-compat", "release-check", "web"}

type policy struct {
	pin   Pin
	units []Obligation
	files map[string]string
}

// PolicyDigest is for review/pin preparation only. Approval must be separate
// from this diagnostic computation, before using that revision as controller policy.
func PolicyDigest(ctx context.Context, r *Repository, commit string) (string, error) {
	p, err := loadPolicy(ctx, r, commit)
	if err != nil {
		return "", err
	}
	return p.pin.Digest, nil
}

func loadPolicy(ctx context.Context, r *Repository, commit string) (policy, error) {
	s, err := r.Snapshot(ctx, commit)
	if err != nil {
		return policy{}, err
	}
	p := policy{pin: Pin{Commit: commit}, files: map[string]string{}}
	parts := [][]byte{[]byte(commit), []byte("shadow/full-tree/fresh-only/v1"), contractBytes}
	var workflow []byte
	for _, path := range policyPaths {
		b, err := r.blob(ctx, s, path)
		if err != nil {
			return policy{}, err
		}
		parts = append(parts, []byte(path), b)
		p.files[path] = Hash("blob", b)
		if path == policyPaths[0] {
			workflow = b
		}
	}
	p.pin.Digest = Hash("policy", parts...)
	var doc struct {
		Jobs map[string]yaml.Node `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(workflow, &doc); err != nil || len(doc.Jobs) == 0 {
		return policy{}, fmt.Errorf("missing or invalid trusted workflow inventory")
	}
	for _, required := range requiredContexts {
		if _, ok := doc.Jobs[required]; !ok {
			return policy{}, fmt.Errorf("required context missing: %s", required)
		}
	}
	if _, ok := doc.Jobs["go-test"]; !ok {
		return policy{}, fmt.Errorf("trusted Go test job missing")
	}
	for name, node := range doc.Jobs {
		definition, err := yaml.Marshal(node)
		if err != nil {
			return policy{}, err
		}
		context := name
		if name == "go-static" || name == "go-timing" || name == "go-test" {
			context = "go"
		}
		p.units = append(p.units, Obligation{Schema: ObligationSchema, ID: "job/" + name, Context: context, Kind: "job", Job: name, DefinitionDigest: Hash("job-definition", definition)})
	}
	// Preserve every row of every approved layout, including all rows of split
	// packages. These are inventory obligations, not an instruction to run both
	// layouts: the existing runner chooses its layout; shadow records every row.
	for layout, path := range policyPaths[1:] {
		count := 7
		if layout == 1 {
			count = 4
		}
		b, err := r.blob(ctx, s, path)
		if err != nil {
			return policy{}, err
		}
		rows := 0
		seen := map[string]bool{}
		shards := map[int]bool{}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			f := strings.Fields(line)
			if len(f) != 3 && len(f) != 4 {
				return policy{}, fmt.Errorf("invalid trusted shard row")
			}
			shard, e1 := strconv.Atoi(f[0])
			ms, e2 := strconv.Atoi(f[1])
			if e1 != nil || e2 != nil || ms < 0 || shard < 1 || shard > count || !strings.HasPrefix(f[2], "github.com/inspr-at/paimos/") {
				return policy{}, fmt.Errorf("invalid trusted shard identity")
			}
			test := ""
			if len(f) == 4 {
				test = f[3]
			}
			key := f[2] + "\x00" + test
			if seen[key] {
				return policy{}, fmt.Errorf("duplicate trusted shard identity")
			}
			seen[key] = true
			shards[shard] = true
			id := fmt.Sprintf("go/%d/%d/%s", count, shard, Hash("package-row", []byte(key)))
			p.units = append(p.units, Obligation{Schema: ObligationSchema, ID: id, Context: "go", Kind: "go-row", Job: "go-test", Shard: shard, Package: f[2], Test: test, DefinitionDigest: Hash("go-row", []byte(line))})
			rows++
		}
		if rows == 0 || len(shards) != count {
			return policy{}, fmt.Errorf("empty or incomplete trusted shard inventory")
		}
	}
	sort.Slice(p.units, func(i, j int) bool { return p.units[i].ID < p.units[j].ID })
	return p, nil
}

// NewPlan schedules the complete inventory. No caller-supplied graph, path list,
// selection, receipt or candidate policy can narrow it. Unknown input closures
// use the complete tree. This does not authenticate event data or approve code.
func NewPlan(ctx context.Context, r *Repository, pin Pin, b Binding, environmentDigest string) (Plan, error) {
	if err := b.validate(); err != nil {
		return Plan{}, err
	}
	if !digestID.MatchString(pin.Digest) || !digestID.MatchString(environmentDigest) {
		return Plan{}, fmt.Errorf("policy and environment digests required")
	}
	p, err := loadPolicy(ctx, r, pin.Commit)
	if err != nil {
		return Plan{}, err
	}
	if p.pin.Digest != pin.Digest {
		return Plan{}, fmt.Errorf("trusted policy digest mismatch")
	}
	if err := r.ancestor(ctx, pin.Commit, b.Base); err != nil {
		return Plan{}, fmt.Errorf("policy revision must be admitted in the immutable base ancestry")
	}
	base, err := r.Snapshot(ctx, b.Base)
	if err != nil {
		return Plan{}, err
	}
	candidate, err := r.Snapshot(ctx, b.Candidate)
	if err != nil {
		return Plan{}, err
	}
	if err := r.ancestor(ctx, b.Base, b.Candidate); err != nil {
		return Plan{}, err
	}
	if b.Event == "pull_request" {
		if _, err := r.commit(ctx, b.SourceHead); err != nil {
			return Plan{}, err
		}
		if err := r.ancestor(ctx, b.SourceHead, b.Candidate); err != nil {
			return Plan{}, err
		}
	}
	plan := Plan{Schema: PlanSchema, Mode: "shadow", Authority: "diagnostic-only", Binding: b, Base: base, Candidate: candidate, Policy: pin, EnvironmentDigest: environmentDigest, RequiredContexts: append([]string(nil), requiredContexts...), Reasons: []string{"optimization-disabled", "unverified-execution-admission", "whole-tree-input-closure"}}
	for _, path := range policyPaths {
		found := false
		for _, e := range candidate.Entries {
			if e.Path == path && e.ContentDigest == p.files[path] && e.Mode == "100644" {
				found = true
			}
		}
		if !found {
			plan.Reasons = append(plan.Reasons, "candidate-policy-change:"+path)
		}
	}
	// Git-only discovery covers new packages/test sources without executing go
	// list or a candidate collector. Deleted/renamed base sources remain visible.
	packages := map[string]bool{}
	for _, snapshot := range []Snapshot{base, candidate} {
		for _, e := range snapshot.Entries {
			if strings.HasSuffix(e.Path, "_test.go") && !strings.Contains(e.Path, "/testdata/") && !strings.HasPrefix(e.Path, "vendor/") {
				pkg := "github.com/inspr-at/paimos"
				if i := strings.LastIndex(e.Path, "/"); i >= 0 {
					pkg += "/" + e.Path[:i]
				}
				packages[pkg] = true
			}
		}
	}
	for pkg := range packages {
		p.units = append(p.units, Obligation{Schema: ObligationSchema, ID: "go-package/" + pkg, Context: "go", Kind: "go-package", Job: "go-test", Package: pkg, DefinitionDigest: Hash("go-package", []byte(pkg))})
	}
	sort.Slice(p.units, func(i, j int) bool { return p.units[i].ID < p.units[j].ID })
	plan.InventoryDigest = digest("suite-inventory", p.units)
	input := wholeTreeInput(plan)
	for _, u := range p.units {
		u.InputDigest = input
		u.Fingerprint = digest("obligation-input", struct {
			Unit  Obligation
			Input string
		}{u, u.InputDigest})
		u.Action = "run"
		u.State = "pending"
		plan.Obligations = append(plan.Obligations, u)
	}
	plan.ID = planID(plan)
	if err := ValidatePlan(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func wholeTreeInput(p Plan) string {
	return digest("whole-tree-input", struct {
		Commit, Tree, Manifest string
		Binding                Binding
		Policy                 Pin
		Environment            string
	}{p.Candidate.Commit, p.Candidate.Tree, p.Candidate.ManifestDigest, p.Binding, p.Policy, p.EnvironmentDigest})
}

// VerifyPlan independently rebuilds the inventory from pinned Git objects.
// Rehashing a narrowed caller-supplied plan cannot substitute for this check.
func VerifyPlan(ctx context.Context, r *Repository, p Plan) error {
	if err := ValidatePlan(p); err != nil {
		return err
	}
	expected, err := NewPlan(ctx, r, p.Policy, p.Binding, p.EnvironmentDigest)
	if err != nil {
		return err
	}
	if expected.ID != p.ID {
		return fmt.Errorf("plan differs from complete trusted Git inventory")
	}
	return nil
}

// ValidatePlan checks internal consistency only. Use VerifyPlan against the
// controller-owned mirror before recording a plan received from a caller.
func ValidatePlan(p Plan) error {
	if err := validateContract("plan", p); err != nil {
		return err
	}
	if err := p.Binding.validate(); err != nil {
		return err
	}
	if p.ID != planID(p) || p.Binding.Base != p.Base.Commit || p.Binding.Candidate != p.Candidate.Commit {
		return fmt.Errorf("plan identity/binding mismatch")
	}
	for _, s := range []Snapshot{p.Base, p.Candidate} {
		if s.ManifestDigest != digest("tree-manifest", s.Entries) {
			return fmt.Errorf("tree manifest mismatch")
		}
		for i, e := range s.Entries {
			if i > 0 && e.Path <= s.Entries[i-1].Path {
				return fmt.Errorf("tree inventory not sorted and unique")
			}
		}
	}
	if strings.Join(p.RequiredContexts, "\x00") != strings.Join(requiredContexts, "\x00") {
		return fmt.Errorf("required contexts narrowed")
	}
	seenContexts := map[string]bool{}
	units := make([]Obligation, 0, len(p.Obligations))
	expectedInput := wholeTreeInput(p)
	for i, u := range p.Obligations {
		if i > 0 && u.ID <= p.Obligations[i-1].ID {
			return fmt.Errorf("duplicate or unsorted obligation")
		}
		seenContexts[u.Context] = true
		unit := u
		unit.InputDigest = ""
		unit.Fingerprint = ""
		unit.Action = ""
		unit.State = ""
		units = append(units, unit)
		unit.InputDigest = expectedInput
		if u.InputDigest != expectedInput || u.Fingerprint != digest("obligation-input", struct {
			Unit  Obligation
			Input string
		}{unit, expectedInput}) {
			return fmt.Errorf("obligation fingerprint mismatch")
		}
	}
	if p.InventoryDigest != digest("suite-inventory", units) {
		return fmt.Errorf("inventory digest mismatch")
	}
	for _, required := range requiredContexts {
		if !seenContexts[required] {
			return fmt.Errorf("required context inventory missing")
		}
	}
	return nil
}
