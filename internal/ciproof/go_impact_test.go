// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"bytes"
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func goFixtureFiles(t *testing.T) map[string]string {
	t.Helper()
	c := files()
	c["internal/dep/dep.go"] = "package dep\nconst Value=1\n"
	c["internal/reader/reader.go"] = "package reader\n"
	c["internal/reader/reader_test.go"] = "package reader\nimport _ \"" + goModule + "/internal/dep\"\nfunc TestRead() {}\n"
	c["internal/external/external_test.go"] = "package external_test\nimport _ \"" + goModule + "/internal/reader\"\nfunc TestExternal() {}\n"
	c["internal/split/split_test.go"] = "package split\nimport _ \"" + goModule + "/internal/dep\"\nfunc TestFirst(){}\nfunc TestSecond(){}\n"
	c["internal/reader/testdata/runtime.sql"] = "select 1"
	c["fixtures/schema.sql"] = "select 1"
	c["internal/reader/embed.txt"] = "embedded value"
	c["internal/reader/reader.go"] = "package reader\nimport _ \"embed\"\n//go:embed embed.txt\nvar Embedded string\n"
	for _, file := range []string{"kind.go", "kind_test.go"} {
		raw, err := os.ReadFile(filepath.Join("..", "runkind", file))
		if err != nil {
			t.Fatal(err)
		}
		c["internal/runkind/"+file] = string(raw)
	}
	return c
}

func fixtureGoList(t *testing.T, c map[string]string) []byte {
	t.Helper()
	packages := map[string]*goListPackage{}
	for name, src := range c {
		if !strings.HasSuffix(name, ".go") || strings.Contains("/"+name, "/testdata/") {
			continue
		}
		pkg := localGoOwner(name)
		p := packages[pkg]
		if p == nil {
			p = &goListPackage{Dir: analysisRoot + "/" + path.Dir(name), ImportPath: pkg, Module: &goListModule{Path: goModule, Main: true}}
			packages[pkg] = p
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		base := path.Base(name)
		if strings.Contains(src, "//go:build") {
			p.IgnoredGoFiles = append(p.IgnoredGoFiles, base)
		} else if strings.HasSuffix(name, "_test.go") {
			if strings.HasSuffix(file.Name.Name, "_test") {
				p.XTestGoFiles = append(p.XTestGoFiles, base)
			} else {
				p.TestGoFiles = append(p.TestGoFiles, base)
			}
		} else {
			p.GoFiles = append(p.GoFiles, base)
		}
		for _, im := range file.Imports {
			dep, err := strconv.Unquote(im.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(file.Name.Name, "_test") {
				p.XTestImports = append(p.XTestImports, dep)
			} else if strings.HasSuffix(name, "_test.go") {
				p.TestImports = append(p.TestImports, dep)
			} else {
				p.Imports = append(p.Imports, dep)
			}
		}
		if strings.Contains(src, "//go:embed embed.txt") {
			p.EmbedFiles = append(p.EmbedFiles, "embed.txt")
		}
	}
	keys := []string{}
	for pkg := range packages {
		keys = append(keys, pkg)
	}
	sort.Strings(keys)
	var out bytes.Buffer
	for _, pkg := range keys {
		p := packages[pkg]
		sort.Strings(p.GoFiles)
		sort.Strings(p.TestGoFiles)
		sort.Strings(p.XTestGoFiles)
		sort.Strings(p.IgnoredGoFiles)
		sort.Strings(p.Imports)
		sort.Strings(p.TestImports)
		sort.Strings(p.XTestImports)
		if err := json.NewEncoder(&out).Encode(p); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

func fixtureGoMetadata(t *testing.T, s Snapshot, c map[string]string, env string) *GoMetadata {
	t.Helper()
	m, err := DecodeGoMetadata(s, GoAnalysisContext{GOOS: "linux", GOARCH: "amd64", ToolchainDigest: Hash("tool", []byte("pinned")), DependencyDigest: Hash("modules", []byte("pinned")), EnvironmentDigest: env}, fixtureGoList(t, c))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestGoMetadataVariantsKeepActualSourceOwners(t *testing.T) {
	_, p, _, _, _ := impactFixture(t, func(map[string]string) {})
	files := goFixtureFiles(t)
	raw := fixtureGoList(t, files)
	for _, variant := range []goListPackage{
		{ImportPath: goModule + "/internal/reader [" + goModule + "/internal/external.test]", ForTest: goModule + "/internal/external", Dir: analysisRoot + "/internal/reader", GoFiles: []string{"reader.go"}, Module: &goListModule{Path: goModule, Main: true}},
		{ImportPath: goModule + "/internal/external_test [" + goModule + "/internal/external.test]", ForTest: goModule + "/internal/external", Dir: analysisRoot + "/internal/external", GoFiles: []string{"external_test.go"}, Imports: []string{goModule + "/internal/reader"}, Module: &goListModule{Path: goModule, Main: true}},
		{ImportPath: goModule + "/internal/external.test", Name: "main", Dir: analysisRoot + "/internal/external", GoFiles: []string{"/cache/generated_testmain.go"}, Module: &goListModule{Path: goModule, Main: true}},
	} {
		b, err := json.Marshal(variant)
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, b...)
	}
	c := GoAnalysisContext{GOOS: "linux", GOARCH: "amd64", ToolchainDigest: Hash("tool", nil), DependencyDigest: Hash("deps", nil), EnvironmentDigest: p.EnvironmentDigest}
	m, err := DecodeGoMetadata(p.Base, c, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !m.packages[goModule+"/internal/external"].imports[goModule+"/internal/reader"] || m.packages[goModule+"/internal/reader"].dir != "internal/reader" {
		t.Fatal("test binary stole dependency source ownership")
	}
	files["scripts/ignored-probe.go"] = "//go:build ignore\n\npackage main\n"
	s := p.Base
	s.Entries = append(s.Entries, Entry{Path: "scripts/ignored-probe.go", Mode: "100644", Blob: strings.Repeat("a", 40), ContentDigest: Hash("blob", []byte(files["scripts/ignored-probe.go"]))})
	sort.Slice(s.Entries, func(i, j int) bool { return s.Entries[i].Path < s.Entries[j].Path })
	s.ManifestDigest = digest("tree-manifest", s.Entries)
	if _, err := DecodeGoMetadata(s, c, raw); err != nil {
		t.Fatalf("globally invalidated ignored script forced permanent cold graph: %v", err)
	}
}

func impactFixture(t *testing.T, mutate func(map[string]string)) (*fixture, Plan, *GoMetadata, *GoMetadata, goImpactPolicy) {
	t.Helper()
	f := newFixture(t)
	before := goFixtureFiles(t)
	after := map[string]string{}
	for k, v := range before {
		after[k] = v
	}
	mutate(after)
	base := f.commit(before, "base")
	head := f.commit(after, "candidate", base)
	d, err := PolicyDigest(context.Background(), f.repo, base)
	if err != nil {
		t.Fatal(err)
	}
	env := Hash("environment", []byte("installed-metadata-image"))
	p, err := NewPlan(context.Background(), f.repo, Pin{base, d}, Binding{RepositoryID: 123, Event: "pull_request", DeliveryID: "test-impact", Generation: 1, Base: base, Candidate: head, SourceHead: head, CheckTarget: head, PR: 1}, env)
	if err != nil {
		t.Fatal(err)
	}
	bm := fixtureGoMetadata(t, p.Base, before, env)
	cm := fixtureGoMetadata(t, p.Candidate, after, env)
	policy := goImpactPolicy{Schema: "aeon.ci.go-impact-policy.v1", Revision: "fixture-audits"}
	for pkg := range bm.packages {
		a := goAudit{Package: pkg, Audit: "Reviewed deterministic fixture; declared filesystem inputs only"}
		dir := strings.TrimPrefix(pkg, goModule+"/") + "/"
		for _, e := range p.Base.Entries {
			if strings.HasPrefix(e.Path, dir) {
				a.Files = append(a.Files, goAuditFile{e.Path, e.ContentDigest})
			}
		}
		if pkg == goModule+"/internal/reader" {
			a.RuntimePaths = []string{"fixtures/schema.sql"}
		}
		policy.Audits = append(policy.Audits, a)
	}
	sort.Slice(policy.Audits, func(i, j int) bool { return policy.Audits[i].Package < policy.Audits[j].Package })
	return f, p, bm, cm, policy
}

func packageImpact(t *testing.T, r GoImpactReport, pkg string) GoPackageImpact {
	t.Helper()
	for _, p := range r.Packages {
		if p.Package == goModule+"/internal/"+pkg {
			return p
		}
	}
	t.Fatalf("missing package %s", pkg)
	return GoPackageImpact{}
}

func TestGoImpactAdversarialInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]string)
		want   string
	}{
		{"production", func(c map[string]string) { c["internal/dep/dep.go"] = "package dep\nconst Value=2\n" }, "reader"},
		{"runtime-without-import-change", func(c map[string]string) { c["fixtures/schema.sql"] = "select 2" }, "reader"},
		{"testdata", func(c map[string]string) { c["internal/reader/testdata/runtime.sql"] = "select 2" }, "reader"},
		{"production-embed", func(c map[string]string) { c["internal/reader/embed.txt"] = "changed embedded input" }, "reader"},
		{"deleted-test", func(c map[string]string) { delete(c, "internal/reader/reader_test.go") }, "reader"},
		{"renamed-test", func(c map[string]string) {
			c["internal/reader/renamed_test.go"] = c["internal/reader/reader_test.go"]
			delete(c, "internal/reader/reader_test.go")
		}, "reader"},
		{"new-test", func(c map[string]string) { c["internal/reader/new_test.go"] = "package reader\nfunc TestNew(){}\n" }, "reader"},
		{"new-package", func(c map[string]string) { c["internal/added/added_test.go"] = "package added\nfunc TestAdded(){}\n" }, "added"},
		{"build-tag", func(c map[string]string) {
			c["internal/reader/tagged_test.go"] = "//go:build special\n\npackage reader\nfunc TestTagged(){}\n"
		}, "reader"},
		{"removed-import", func(c map[string]string) {
			c["internal/reader/reader_test.go"] = "package reader\nfunc TestRead(){}\n"
			c["internal/dep/dep.go"] = "package dep\nconst Value=2\n"
		}, "reader"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, p, b, c, policy := impactFixture(t, tc.mutate)
			r := analyzeGoImpact(p, b, c, policy, digest("fixture-policy", policy))
			if r.FullSelection {
				t.Fatalf("mapped change unexpectedly full: %v", r.Reasons)
			}
			if !packageImpact(t, r, tc.want).Selected {
				t.Fatal("affected package omitted")
			}
			if tc.want == "reader" && !packageImpact(t, r, "external").Selected {
				t.Fatal("XTest reverse dependency omitted")
			}
			if packageImpact(t, r, "runkind").Selected {
				t.Fatal("unrelated audited leaf selected")
			}
			for _, u := range p.Obligations {
				if u.Action != "run" || u.State != "pending" {
					t.Fatal("full execution plan changed")
				}
			}
			if !slices.Contains(r.RequiredFreshPackages, goModule+"/internal/runkind") {
				t.Fatal("cold baseline omitted unchanged leaf from actual work")
			}
		})
	}
}

func TestGoImpactPreservesCompleteSplitRowsAndTiming(t *testing.T) {
	_, p, b, c, policy := impactFixture(t, func(c map[string]string) { c["internal/dep/dep.go"] = "package dep\nconst Value=2\n" })
	r := analyzeGoImpact(p, b, c, policy, digest("fixture-policy", policy))
	want := []Obligation{}
	got := []Obligation{}
	for _, u := range p.Obligations {
		if u.Kind == "go-row" && u.Package == goModule+"/internal/split" {
			want = append(want, u)
		}
	}
	for _, u := range r.SelectedRows {
		if u.Package == goModule+"/internal/split" {
			got = append(got, u)
		}
	}
	if digest("rows", want) != digest("rows", got) || len(got) != 4 {
		t.Fatal("lost or rewrote split-package rows in either layout")
	}
	for _, job := range []string{"job/go", "job/go-test", "job/go-static", "job/go-timing", "job/web", "job/e2e", "job/migration-compat"} {
		if !slices.Contains(r.AlwaysRunJobs, job) {
			t.Fatalf("job omitted %s", job)
		}
	}
}

func TestGoImpactFallbacksAndAuditDrift(t *testing.T) {
	for _, file := range []string{".github/other.yml", "scripts/untrusted.json", "go.sum", "internal/db/migrations/9999_probe.sql", "api/openapi.yaml", "web/dist/app.js", "internal/webembed/bundle.bin", "version.json", "unknown/new-input.txt"} {
		t.Run(file, func(t *testing.T) {
			_, p, b, c, policy := impactFixture(t, func(c map[string]string) { c[file] = "changed" })
			r := analyzeGoImpact(p, b, c, policy, digest("fixture-policy", policy))
			if !r.FullSelection {
				t.Fatal("global/unmapped input narrowed work")
			}
			for _, pkg := range r.Packages {
				if !pkg.Selected {
					t.Fatal("full fallback omitted package")
				}
			}
		})
	}
	_, p, b, c, policy := impactFixture(t, func(c map[string]string) {})
	for _, pair := range [][2]*GoMetadata{{nil, c}, {b, nil}} {
		r := analyzeGoImpact(p, pair[0], pair[1], policy, digest("fixture-policy", policy))
		if !r.FullSelection {
			t.Fatal("cold analysis did not fall back")
		}
	}
	p.Binding.Event = "merge_group"
	r := analyzeGoImpact(p, b, c, policy, digest("fixture-policy", policy))
	if !r.FullSelection {
		t.Fatal("queue narrowed")
	}
	p.Binding.Event = "push"
	r = analyzeGoImpact(p, b, c, policy, digest("fixture-policy", policy))
	if !r.FullSelection {
		t.Fatal("main narrowed")
	}
	p.Binding.Event = "pull_request"
	policy.Audits = nil
	r = analyzeGoImpact(p, b, c, policy, digest("fixture-policy", policy))
	if !packageImpact(t, r, "runkind").Selected {
		t.Fatal("missing audit allowed omission")
	}
}

func TestCompiledGoAuditAndCandidateMetadataCannotNarrowSourceImports(t *testing.T) {
	f, p, b, c, _ := impactFixture(t, func(c map[string]string) { c["internal/dep/dep.go"] = "package dep\nconst Value=2\n" })
	// Simulate forged metadata removing both production and test import edges.
	for _, m := range []*GoMetadata{b, c} {
		for _, g := range m.packages {
			g.imports = map[string]bool{}
		}
	}
	r, err := AnalyzeGoImpact(context.Background(), f.repo, p, b, c)
	if err != nil {
		t.Fatal(err)
	}
	if r.FullSelection || packageImpact(t, r, "runkind").Selected {
		t.Fatalf("compiled audit not usable: %v", r.Reasons)
	}
	reader := packageImpact(t, r, "reader")
	if !slices.Contains(reader.Reasons, "reverse-dependency:"+goModule+"/internal/dep") {
		t.Fatalf("Git imports were narrowed: %v", reader.Reasons)
	}
	if err := VerifyGoImpact(context.Background(), f.repo, p, b, c, r); err != nil {
		t.Fatal(err)
	}
	r.Packages = nil
	r.ID = goImpactID(r)
	if VerifyGoImpact(context.Background(), f.repo, p, b, c, r) == nil {
		t.Fatal("rehashing narrowed report bypassed reconstruction")
	}
}

func TestGoMetadataRejectsUnsupportedAndIncompleteArtifacts(t *testing.T) {
	_, p, base, _, _ := impactFixture(t, func(c map[string]string) {})
	c := GoAnalysisContext{GOOS: "linux", GOARCH: "amd64", ToolchainDigest: Hash("tool", nil), DependencyDigest: Hash("deps", nil), EnvironmentDigest: p.EnvironmentDigest}
	for _, raw := range []string{"", `{}`, `{"ImportPath":"a","ImportPath":"b"}`, `{"ImportPath":"a","Error":{}}`, `{"ImportPath":"a","Incomplete":true}`, `{"ImportPath":"a","Module":{"Replace":{}}}`} {
		if _, err := DecodeGoMetadata(p.Base, c, []byte(raw)); err == nil {
			t.Fatalf("bad metadata accepted %s", raw)
		}
	}
	// An empty local graph, even with plausible package identity, loses files.
	raw, _ := json.Marshal(goListPackage{ImportPath: goModule + "/internal/reader", Dir: analysisRoot + "/internal/reader", Module: &goListModule{Path: goModule, Main: true}})
	if _, err := DecodeGoMetadata(p.Base, c, raw); err == nil {
		t.Fatal("missing immutable source inventory accepted")
	}
	for _, mutate := range []func(*GoAnalysisContext){func(c *GoAnalysisContext) { c.GOOS = "darwin" }, func(c *GoAnalysisContext) { c.GOARCH = "arm64" }, func(c *GoAnalysisContext) { c.Tags = []string{"special"} }, func(c *GoAnalysisContext) { c.ToolchainDigest = "" }} {
		copy := c
		mutate(&copy)
		if _, err := DecodeGoMetadata(p.Base, copy, raw); err == nil {
			t.Fatal("unsupported context accepted")
		}
	}
	base.environment = Hash("other-env", nil)
	r := analyzeGoImpact(p, base, base, goImpactPolicy{}, Hash("policy", nil))
	if !r.FullSelection {
		t.Fatal("wrong context narrowed")
	}
	if _, err := SupervisedGoMetadata(nil, p, SupervisedObservation{}, c); err == nil {
		t.Fatal("unsealed artifact accepted")
	}
	recipe := GoMetadataRecipe("job/go-test")
	if !validRecipe(recipe) || recipe.Stage != "metadata" || recipe.Argv[0] != "/opt/aeon/bin/go" || !slices.Contains(recipe.Argv, "-mod=readonly") {
		t.Fatal("unsafe metadata recipe")
	}
}

func fullGoFixtureRun(p Plan, r GoImpactReport) GoFullRun {
	live := map[string]bool{}
	for _, pkg := range r.Packages {
		live[pkg.Package] = pkg.CandidatePresent
	}
	run := GoFullRun{Schema: "aeon.ci.go-full-run.v1", PlanID: p.ID, CandidateCommit: p.Candidate.Commit, EnvironmentDigest: p.EnvironmentDigest, RunID: 789, Attempt: 1, Layout: 7, Results: []GoRowResult{}}
	rows := map[string]bool{}
	for _, u := range p.Obligations {
		if u.Kind == "go-row" && strings.HasPrefix(u.ID, "go/7/") && live[u.Package] {
			rows[u.Package] = true
			run.Results = append(run.Results, GoRowResult{u.ID, u.Package, "success"})
		}
	}
	for _, u := range p.Obligations {
		if u.Kind == "go-package" && live[u.Package] && !rows[u.Package] {
			run.Results = append(run.Results, GoRowResult{u.ID, u.Package, "success"})
		}
	}
	return run
}

func TestGoFullComparisonRequiresEveryRowAndReportsOmittedFailures(t *testing.T) {
	f, p, b, c, _ := impactFixture(t, func(c map[string]string) { c["internal/dep/dep.go"] = "package dep\nconst Value=2\n" })
	r, err := AnalyzeGoImpact(context.Background(), f.repo, p, b, c)
	if err != nil {
		t.Fatal(err)
	}
	run := fullGoFixtureRun(p, r)
	compare, err := CompareGoImpact(p, r, run)
	if err != nil || !compare.Complete || compare.OmittedFailureCount == nil || *compare.OmittedFailureCount != 0 {
		t.Fatalf("full comparison %v %v", compare, err)
	}
	for i := range run.Results {
		if run.Results[i].Package == goModule+"/internal/runkind" {
			run.Results[i].Result = "failure"
		}
	}
	compare, err = CompareGoImpact(p, r, run)
	if err != nil || compare.Verdict != "omitted-failures" || *compare.OmittedFailureCount != 1 {
		t.Fatalf("omitted failure not reported %v %v", compare, err)
	}
	run.Results = run.Results[1:]
	compare, err = CompareGoImpact(p, r, run)
	if err != nil || compare.Complete || compare.OmittedFailureCount != nil {
		t.Fatal("missing row claimed zero omitted failures")
	}
	run = fullGoFixtureRun(p, r)
	run.Results[0].Result = "skipped"
	compare, err = CompareGoImpact(p, r, run)
	if err != nil || compare.Complete || compare.OmittedFailureCount != nil {
		t.Fatal("skipped result claimed complete comparison")
	}
	for _, mutate := range []func(*GoFullRun){func(r *GoFullRun) { r.Attempt = 2 }, func(r *GoFullRun) { r.RunID = 0 }, func(r *GoFullRun) { r.CandidateCommit = strings.Repeat("f", 40) }, func(r *GoFullRun) { r.Layout = 4 }, func(r *GoFullRun) { r.Results = append(r.Results, r.Results[0]) }, func(r *GoFullRun) { r.Results[0].Package = "wrong" }, func(r *GoFullRun) { r.Results[0].Result = "unknown" }} {
		run := fullGoFixtureRun(p, r)
		mutate(&run)
		if _, err := CompareGoImpact(p, r, run); err == nil {
			t.Fatal("forged/partial-run result accepted")
		}
	}
	file := filepath.Join(t.TempDir(), "go-shadow.jsonl")
	run = fullGoFixtureRun(p, r)
	written, comparison, err := RecordGoImpact(context.Background(), f.repo, file, p, b, c, &run)
	if err != nil || comparison == nil || !comparison.Complete || written.ID != r.ID {
		t.Fatalf("record %v", err)
	}
	if _, _, err := RecordGoImpact(context.Background(), f.repo, file, p, b, c, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || bytes.Count(data, []byte{'\n'}) != 2 {
		t.Fatal("shadow records missing")
	}
	if err := os.WriteFile(file, []byte("truncated"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RecordGoImpact(context.Background(), f.repo, file, p, b, c, nil); err == nil {
		t.Fatal("truncated record file accepted")
	}
}
