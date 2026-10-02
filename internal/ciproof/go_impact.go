// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"context"
	_ "embed"
	"fmt"
	"path"
	"sort"
	"strings"
)

// This compiled policy is installed/reviewed with the controller. Never read a
// candidate copy as policy or let candidate declarations narrow runtime inputs.
//
//go:embed go-impact-policy.json
var goImpactPolicyBytes []byte

type goAuditFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}
type goAudit struct {
	Package      string        `json:"package"`
	Audit        string        `json:"audit"`
	Files        []goAuditFile `json:"files"`
	RuntimePaths []string      `json:"runtime_paths"`
}
type goImpactPolicy struct {
	Schema   string    `json:"schema"`
	Revision string    `json:"revision"`
	Audits   []goAudit `json:"audits"`
}

type GoPackageImpact struct {
	Package          string   `json:"package"`
	CandidatePresent bool     `json:"candidate_present"`
	Selected         bool     `json:"selected"`
	InputDigest      string   `json:"input_digest"`
	Reasons          []string `json:"reasons"`
}

// GoImpactReport is a hypothetical scheduling hint beside the unchanged full
// Plan. It has no action/state field and cannot be submitted as an execution
// plan. Every live package, ordinary shard row and timing/static job still runs.
// Baseline receipts/reuse are E's scope; a cold or invalidated baseline therefore
// has RequiredFreshPackages equal to the complete candidate package inventory.
type GoImpactReport struct {
	Schema                  string            `json:"schema"`
	ID                      string            `json:"id"`
	Mode                    string            `json:"mode"`
	Authority               string            `json:"authority"`
	PlanID                  string            `json:"plan_id"`
	PolicyDigest            string            `json:"policy_digest"`
	AnalysisDigest          string            `json:"analysis_digest"`
	BaseMetadataDigest      string            `json:"base_metadata_digest,omitempty"`
	CandidateMetadataDigest string            `json:"candidate_metadata_digest,omitempty"`
	FullSelection           bool              `json:"full_selection"`
	Reasons                 []string          `json:"reasons"`
	Packages                []GoPackageImpact `json:"packages"`
	SelectedRows            []Obligation      `json:"selected_rows"`
	RequiredFreshPackages   []string          `json:"required_fresh_packages"`
	AlwaysRunJobs           []string          `json:"always_run_jobs"`
}

func goImpactID(r GoImpactReport) string { r.ID = ""; return digest("go-impact-shadow", r) }

// AnalyzeGoImpact never changes the foundation/controller's complete plan.
// Invalid graphs, inputs or event eligibility schedule the full shadow inventory.
// Metadata errors must be passed as nil, not replaced by a smaller graph.
func AnalyzeGoImpact(ctx context.Context, r *Repository, p Plan, base, candidate *GoMetadata) (GoImpactReport, error) {
	if err := VerifyPlan(ctx, r, p); err != nil {
		return GoImpactReport{}, err
	}
	var policy goImpactPolicy
	if err := decodeStrict(goImpactPolicyBytes, &policy); err != nil {
		return GoImpactReport{}, err
	}
	// Failed or oversized analysis is a full hint, never permission to omit.
	if base != nil && len(base.packages)*len(p.Base.Entries) <= 5000000 {
		base, _ = confirmGoImports(ctx, r, p.Base, base)
	} else {
		base = nil
	}
	if candidate != nil && len(candidate.packages)*len(p.Candidate.Entries) <= 5000000 {
		candidate, _ = confirmGoImports(ctx, r, p.Candidate, candidate)
	} else {
		candidate = nil
	}
	return analyzeGoImpact(p, base, candidate, policy, Hash("go-impact-policy", goImpactPolicyBytes)), nil
}

func globalGoInput(file string) bool {
	for _, prefix := range []string{".github/", "scripts/", "vendor/", "internal/db/migrations/", "api/", "web/", "internal/webembed/", "internal/ciproof/", "internal/ciexecutor/", "docs/releases/", "releases/", "nix/"} {
		if strings.HasPrefix(file, prefix) {
			return true
		}
	}
	switch path.Base(file) {
	case "go.mod", "go.sum", "go.work", "go.work.sum":
		return true
	}
	switch file {
	case "Dockerfile", "docker-compose.yml", "compose.yml", "justfile", "flake.nix", "flake.lock", "version.json":
		return true
	}
	return strings.HasPrefix(file, "version") || strings.HasPrefix(file, "release")
}

func changedGoEntries(base, candidate Snapshot) []string {
	before := map[string]Entry{}
	after := map[string]Entry{}
	changed := map[string]bool{}
	for _, e := range base.Entries {
		before[e.Path] = e
	}
	for _, e := range candidate.Entries {
		after[e.Path] = e
	}
	for name, e := range before {
		if after[name] != e {
			changed[name] = true
		}
	}
	for name, e := range after {
		if before[name] != e {
			changed[name] = true
		}
	}
	return goKeys(changed)
}

func pathWithin(file, input string) bool {
	if strings.HasSuffix(input, "/") {
		return strings.HasPrefix(file, input)
	}
	return file == input
}

func analyzeGoImpact(p Plan, base, candidate *GoMetadata, policy goImpactPolicy, policyDigest string) GoImpactReport {
	report := GoImpactReport{Schema: "aeon.ci.go-impact-shadow.v1", Mode: "shadow", Authority: "diagnostic-only", PlanID: p.ID, PolicyDigest: policyDigest,
		Reasons: []string{"optimization-disabled", "baseline-proof-unavailable:full-fresh-execution"}, Packages: []GoPackageImpact{}, SelectedRows: []Obligation{}, RequiredFreshPackages: []string{}, AlwaysRunJobs: []string{}}
	inventory := map[string]bool{}
	for _, u := range p.Obligations {
		if u.Package != "" && u.Context == "go" {
			inventory[u.Package] = true
		}
		if u.Kind == "job" {
			report.AlwaysRunJobs = append(report.AlwaysRunJobs, u.ID)
		}
	}
	forced := map[string]map[string]bool{}
	mark := func(pkg, reason string) {
		if forced[pkg] == nil {
			forced[pkg] = map[string]bool{}
		}
		forced[pkg][reason] = true
	}
	full := func(reason string) { report.FullSelection = true; report.Reasons = append(report.Reasons, reason) }
	if p.Binding.Event != "pull_request" {
		full("event-requires-full-work")
	}
	valid := base != nil && candidate != nil && base.commit == p.Base.Commit && candidate.commit == p.Candidate.Commit && base.manifest == p.Base.ManifestDigest && candidate.manifest == p.Candidate.ManifestDigest && base.contextDigest == candidate.contextDigest && base.environment == p.EnvironmentDigest && candidate.environment == p.EnvironmentDigest
	if !valid {
		full("missing-invalid-or-mismatched-analysis")
	}
	if valid {
		report.AnalysisDigest = base.contextDigest
		report.BaseMetadataDigest = base.outputDigest
		report.CandidateMetadataDigest = candidate.outputDigest
		reverse := map[string]map[string]bool{}
		owners := map[string]map[string]bool{}
		// Union both graphs, including test variants and removed edges. Embed
		// files can have multiple owners. testdata is a package-local subtree.
		for _, m := range []*GoMetadata{base, candidate} {
			for pkg, g := range m.packages {
				for dep := range g.imports {
					if reverse[dep] == nil {
						reverse[dep] = map[string]bool{}
					}
					reverse[dep][pkg] = true
				}
				for file := range g.files {
					if owners[file] == nil {
						owners[file] = map[string]bool{}
					}
					owners[file][pkg] = true
				}
				for _, s := range []Snapshot{p.Base, p.Candidate} {
					for _, e := range s.Entries {
						prefix := "testdata/"
						if g.dir != "" {
							prefix = g.dir + "/" + prefix
						}
						if strings.HasPrefix(e.Path, prefix) {
							if owners[e.Path] == nil {
								owners[e.Path] = map[string]bool{}
							}
							owners[e.Path][pkg] = true
						}
					}
				}
			}
		}
		for _, a := range policy.Audits {
			for _, runtime := range a.RuntimePaths {
				for _, s := range []Snapshot{p.Base, p.Candidate} {
					for _, e := range s.Entries {
						if pathWithin(e.Path, runtime) {
							if owners[e.Path] == nil {
								owners[e.Path] = map[string]bool{}
							}
							owners[e.Path][a.Package] = true
						}
					}
				}
			}
		}
		for _, file := range changedGoEntries(p.Base, p.Candidate) {
			if globalGoInput(file) {
				full("global-input:" + file)
				continue
			}
			if len(owners[file]) == 0 {
				full("unmapped-input:" + file)
				continue
			}
			for pkg := range owners[file] {
				mark(pkg, "changed-input:"+file)
			}
		}
		// A runtime/fixture/source change travels through production and test
		// imports in either revision, even when the candidate removes an edge.
		queue := []string{}
		visited := map[string]bool{}
		for pkg := range forced {
			queue = append(queue, pkg)
			visited[pkg] = true
		}
		for n := 0; n < len(queue); n++ {
			for dependent := range reverse[queue[n]] {
				mark(dependent, "reverse-dependency:"+queue[n])
				if !visited[dependent] {
					visited[dependent] = true
					queue = append(queue, dependent)
				}
			}
		}
		audits := map[string]goAudit{}
		for _, a := range policy.Audits {
			audits[a.Package] = a
		}
		for pkg := range inventory {
			if candidate.packages[pkg] == nil {
				mark(pkg, "deleted-package")
			}
			if base.packages[pkg] == nil {
				mark(pkg, "new-package")
			}
			// Every local dependency of a hypothetically omitted package must
			// have a byte-pinned runtime audit. Unknown/dynamic readers are run.
			closure := goClosure(pkg, base, candidate)
			for dep := range closure {
				if !auditMatches(audits[dep], p.Base) || !auditMatches(audits[dep], p.Candidate) {
					mark(pkg, "unaudited-runtime-closure:"+dep)
				}
			}
		}
	}
	for _, pkg := range goKeys(inventory) {
		present := candidate != nil && candidate.packages[pkg] != nil
		// Without usable metadata, derive presence from immutable Go sources.
		if !valid {
			present = false
			for _, e := range p.Candidate.Entries {
				if strings.HasSuffix(e.Path, ".go") && localGoOwner(e.Path) == pkg {
					present = true
				}
			}
		}
		input := wholeTreeInput(p)
		if valid {
			input = goClosureDigest(p, pkg, base, candidate, policy, policyDigest)
		}
		reasons := goKeys(forced[pkg])
		selected := report.FullSelection || len(reasons) > 0
		if report.FullSelection {
			reasons = append(reasons, "full-selection")
		}
		if !selected {
			reasons = append(reasons, "audited-unchanged:hint-only")
		}
		report.Packages = append(report.Packages, GoPackageImpact{pkg, present, selected, input, reasons})
		if present {
			report.RequiredFreshPackages = append(report.RequiredFreshPackages, pkg)
		}
		if selected {
			for _, u := range p.Obligations {
				if u.Kind == "go-row" && u.Package == pkg {
					report.SelectedRows = append(report.SelectedRows, u)
				}
			}
		}
	}
	sort.Strings(report.Reasons)
	sort.Slice(report.SelectedRows, func(i, j int) bool { return report.SelectedRows[i].ID < report.SelectedRows[j].ID })
	report.ID = goImpactID(report)
	return report
}

func localGoOwner(file string) string {
	dir := path.Dir(file)
	if dir == "." {
		return goModule
	}
	return goModule + "/" + dir
}

func goClosure(pkg string, graphs ...*GoMetadata) map[string]bool {
	out := map[string]bool{pkg: true}
	queue := []string{pkg}
	for n := 0; n < len(queue); n++ {
		for _, m := range graphs {
			for dep := range importsOf(m, queue[n]) {
				if !out[dep] {
					out[dep] = true
					queue = append(queue, dep)
				}
			}
		}
	}
	return out
}
func importsOf(m *GoMetadata, pkg string) map[string]bool {
	if g := m.packages[pkg]; g != nil {
		return g.imports
	}
	return nil
}

func auditMatches(a goAudit, s Snapshot) bool {
	if a.Package == "" || a.Audit == "" || len(a.Files) == 0 {
		return false
	}
	files := map[string]string{}
	for _, f := range a.Files {
		files[f.Path] = f.Digest
	}
	dir := strings.TrimPrefix(a.Package, goModule+"/") + "/"
	seen := 0
	for _, e := range s.Entries {
		if strings.HasPrefix(e.Path, dir) || files[e.Path] != "" {
			// Audit pins use the snapshot's domain-separated Hash(blob) digest.
			if files[e.Path] != e.ContentDigest || (e.Mode != "100644" && e.Mode != "100755") {
				return false
			}
			seen++
		}
	}
	return seen == len(a.Files)
}

func goClosureDigest(p Plan, pkg string, base, candidate *GoMetadata, policy goImpactPolicy, policyDigest string) string {
	inputs := map[string]bool{}
	for dep := range goClosure(pkg, base, candidate) {
		for _, m := range []*GoMetadata{base, candidate} {
			if g := m.packages[dep]; g != nil {
				for file := range g.files {
					inputs[file] = true
				}
				for _, e := range p.Candidate.Entries {
					if strings.HasPrefix(e.Path, g.dir+"/testdata/") {
						inputs[e.Path] = true
					}
				}
			}
		}
		for _, a := range policy.Audits {
			if a.Package == dep {
				for _, runtime := range a.RuntimePaths {
					for _, e := range p.Candidate.Entries {
						if pathWithin(e.Path, runtime) {
							inputs[e.Path] = true
						}
					}
				}
			}
		}
	}
	inputs["go.mod"] = true
	inputs["go.sum"] = true
	entries := []Entry{}
	for _, e := range p.Candidate.Entries {
		if inputs[e.Path] {
			entries = append(entries, e)
		}
	}
	return digest("go-package-input-closure", struct {
		Package, Policy, Analysis, Environment string
		Entries                                []Entry
	}{pkg, policyDigest, base.contextDigest, p.EnvironmentDigest, entries})
}

// VerifyGoImpact rejects caller-edited/narrowed hints by reconstructing them
// from the same immutable plan and private metadata graphs.
func VerifyGoImpact(ctx context.Context, r *Repository, p Plan, base, candidate *GoMetadata, report GoImpactReport) error {
	want, err := AnalyzeGoImpact(ctx, r, p, base, candidate)
	if err != nil {
		return err
	}
	if report.ID != goImpactID(report) || report.ID != want.ID {
		return fmt.Errorf("shadow impact differs from reconstructed inventory")
	}
	return nil
}
