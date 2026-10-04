// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseLogPackageTimes(t *testing.T) {
	log := strings.Join([]string{
		"go\tRun go test ./...\t2026-09-29T23:52:44.4604908Z ?   \tgithub.com/inspr-at/paimos\t[no test files]",
		"go\tRun go test ./...\t2026-09-29T23:53:23.6633979Z ok  \tgithub.com/inspr-at/paimos/cmd/aeon\t34.256s",
		"go\tRun go test ./...\t2026-09-29T23:55:07.6582589Z ok  \tgithub.com/inspr-at/paimos/internal/agentpairing\t121.778s",
		"noise that mentions ok github.com/inspr-at/paimos/internal/ignored 1.000s",
	}, "\n")
	got, err := parseLog(log)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"github.com/inspr-at/paimos":                       0,
		"github.com/inspr-at/paimos/cmd/aeon":              34256,
		"github.com/inspr-at/paimos/internal/agentpairing": 121778,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for path, ms := range want {
		if got[path] != ms {
			t.Fatalf("%s: got %d want %d", path, got[path], ms)
		}
	}
}

func TestParseTestJSONSkipsSubtests(t *testing.T) {
	raw := strings.Join([]string{
		`{"Action":"run","Package":"p","Test":"TestA"}`,
		`{"Action":"pass","Package":"p","Test":"TestA/sub","Elapsed":0.01}`,
		`{"Action":"pass","Package":"p","Test":"TestA","Elapsed":1.5}`,
		`{"Action":"pass","Package":"p","Elapsed":1.5}`,
		`{"Action":"skip","Package":"p","Test":"ExampleB","Elapsed":0}`,
	}, "\n")
	got, err := parseTestJSON(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Test != "TestA" || got[0].MS != 1500 || got[1].Test != "ExampleB" || got[1].MS != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestScaleToPreservesTheSum(t *testing.T) {
	local := []Item{{Path: "p", Test: "TestA", MS: 1}, {Path: "p", Test: "TestB", MS: 3}}
	got, err := scaleTo(local, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	sum := got[0].MS + got[1].MS
	if sum != 10_000 {
		t.Fatalf("sum %d items %+v", sum, got)
	}
	if got[0].MS != 2500 || got[1].MS != 7500 {
		t.Fatalf("%+v", got)
	}
}

func TestBalanceLongestFirst(t *testing.T) {
	items := []Item{
		{MS: 10, Path: "a"},
		{MS: 9, Path: "b"},
		{MS: 8, Path: "c"},
		{MS: 7, Path: "d"},
		{MS: 6, Path: "e"},
		{MS: 5, Path: "f"},
	}
	got, err := balance(items, 4)
	if err != nil {
		t.Fatal(err)
	}
	load := map[int]int{}
	shard := map[string]int{}
	for _, it := range got {
		load[it.Shard] += it.MS
		shard[it.Path] = it.Shard
	}
	if load[1] != 10 || load[2] != 9 || load[3] != 13 || load[4] != 13 {
		t.Fatalf("loads %v assignment %v", load, shard)
	}
	if shard["a"] != 1 || shard["b"] != 2 || shard["c"] != 3 || shard["d"] != 4 || shard["e"] != 4 || shard["f"] != 3 {
		t.Fatalf("assignment %v", shard)
	}
}

func TestBalanceSpreadsZeroWeights(t *testing.T) {
	var items []Item
	for _, path := range []string{"a", "b", "c", "d", "e"} {
		items = append(items, Item{Path: path})
	}
	got, err := balance(items, 4)
	if err != nil {
		t.Fatal(err)
	}
	count := map[int]int{}
	for _, it := range got {
		count[it.Shard]++
	}
	if count[1] != 2 || count[2] != 1 || count[3] != 1 || count[4] != 1 {
		t.Fatalf("counts %v", count)
	}
}

func TestBuildItemsSplitsOnlyLongPackages(t *testing.T) {
	pkgs := map[string]int{"short": 1000, "long": 2000}
	tests := []Item{{Path: "long", Test: "TestA", MS: 1}, {Path: "long", Test: "TestB", MS: 1}}
	got, err := buildItems(pkgs, tests, 1500)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	sum := 0
	for _, it := range got {
		if it.Path == "long" {
			sum += it.MS
			if it.Test == "" {
				t.Fatalf("long package was not split: %+v", got)
			}
		}
	}
	if sum != 2000 {
		t.Fatalf("scaled sum %d", sum)
	}
}

func TestFileRoundTripAndValidate(t *testing.T) {
	items := []Item{
		{MS: 10, Path: "a"}, {MS: 9, Path: "b"}, {MS: 8, Path: "c"}, {MS: 7, Path: "d"},
		{MS: 4, Path: "long", Test: "TestA"}, {MS: 4, Path: "long", Test: "TestB"},
		{MS: 3, Path: "long", Test: "TestC"}, {MS: 3, Path: "long", Test: "TestD"},
	}
	assigned, err := balance(items, hostedShardCount)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseFile(formatFile(assigned, 5, hostedShardCount), hostedShardCount)
	if err != nil {
		t.Fatal(err)
	}
	if err := validate(parsed, 100, hostedShardCount); err != nil {
		t.Fatal(err)
	}
	parsed[0].Shard = parsed[0].Shard%hostedShardCount + 1
	if err := validate(parsed, 100, hostedShardCount); err == nil {
		t.Fatal("hand-edited shard was accepted")
	}
}

func TestRunRegexIsAnchored(t *testing.T) {
	got, err := runRegex([]string{"TestB", "TestA"})
	if err != nil {
		t.Fatal(err)
	}
	if got != `^(TestA|TestB)$` {
		t.Fatalf("%s", got)
	}
	if _, err := runRegex([]string{"TestA/sub"}); err == nil {
		t.Fatal("subtest name was accepted")
	}
	got, err = runRegex([]string{"FuzzNew", "Example_new", "TestNew"})
	if err != nil {
		t.Fatal(err)
	}
	if got != `^(Example_new|FuzzNew|TestNew)$` {
		t.Fatalf("%s", got)
	}
	if _, err := runRegex([]string{"BenchmarkHi"}); err == nil {
		t.Fatal("benchmark was accepted")
	}
}

func TestAlignPackageTimesCarriesNewPackages(t *testing.T) {
	got, err := alignPackageTimes([]string{"a", "b"}, map[string]int{"a": 10})
	if err != nil {
		t.Fatal(err)
	}
	if got["a"] != 10 || got["b"] != 0 {
		t.Fatalf("%v", got)
	}
	if _, err := alignPackageTimes([]string{"a"}, map[string]int{"a": 1, "gone": 2}); err == nil {
		t.Fatal("stale package was accepted")
	}
}

func TestTestNamesIn(t *testing.T) {
	src := []byte(`package p
func TestA(t *testing.T) {}
func TestMain(m *testing.M) {}
func Testb(t *testing.T) {}
func Example() {}
func Example_foo() {}
func Examplebad() {}
func (s *S) TestNope(t *testing.T) {}
func BenchmarkHi(b *testing.B) {}
func FuzzA(f *testing.F) {}
func Fuzz(f *testing.F) {}
func Fuzzbad(f *testing.F) {}
func Test(t *testing.T) {}
`)
	got, err := testNamesIn("p_test.go", src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"TestA", "Example", "Example_foo", "FuzzA", "Fuzz", "Test"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v", got)
	}
}

func TestShardNeedsShellOnlyForPathProof(t *testing.T) {
	items := []Item{
		{Shard: 1, Path: pairingPackage, Test: "TestPairingGuideReleaseContract"},
		{Shard: 2, Path: pairingPackage, Test: pathProofTest},
		{Shard: 3, Path: "other"},
	}
	if shardNeedsShell(items, 1) || !shardNeedsShell(items, 2) || shardNeedsShell(items, 3) {
		t.Fatalf("split assignment")
	}
	whole := []Item{{Shard: 4, Path: pairingPackage}}
	if !shardNeedsShell(whole, 4) || shardNeedsShell(whole, 5) {
		t.Fatalf("whole package")
	}
}

func TestWorkflowShardLayouts(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Needs    any               `yaml:"needs"`
			RunsOn   string            `yaml:"runs-on"`
			Env      map[string]string `yaml:"env"`
			Strategy struct {
				Matrix map[string]string `yaml:"matrix"`
			} `yaml:"strategy"`
			Steps []struct {
				Name string
				Uses string            `yaml:"uses"`
				Run  string            `yaml:"run"`
				With map[string]any    `yaml:"with"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	job := workflow.Jobs["go-test"]
	for _, count := range []int{macShardCount, hostedShardCount} {
		nums := make([]string, count)
		for i := range nums {
			nums[i] = strconv.Itoa(i + 1)
		}
		if !strings.Contains(job.Strategy.Matrix["shard"], "["+strings.Join(nums, ", ")+"]") {
			t.Fatalf("matrix missing %d-way layout", count)
		}
	}
	for _, guard := range []string{"github.event_name", "refs/heads/main", "outputs.run_attempt == github.run_attempt"} {
		if !strings.Contains(job.Strategy.Matrix["shard"], guard) {
			t.Fatalf("matrix missing %s", guard)
		}
	}
	if job.Env["AEON_GO_SHARD_COUNT"] != "${{ strategy.job-total }}" || job.Env["GOFLAGS"] != "${{ strategy.job-total == 4 && '-count=1' || '' }}" {
		t.Fatal("shard commands must use the selected count and bypass persistent Mac test results")
	}
	if job.Steps[0].With["persist-credentials"] != false || job.Steps[1].With["cache"] != true || job.Steps[1].With["cache-dependency-path"] != "go.sum" {
		t.Fatal("routed checkout must remain credential-free and Go caching must use go.sum")
	}
	foundCache := false
	foundFreshTests := false
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Name, "Test this shard") {
			tierRunner, err := os.ReadFile(filepath.Join(root, "scripts/test-tiers/cli.mjs"))
			if err != nil {
				t.Fatal(err)
			}
			foundFreshTests = step.Env["GOFLAGS"] == "-count=1" && strings.Contains(step.Run, "cli.mjs run go --shard") && strings.Contains(string(tierRunner), "'-count=1'")
		}
		if strings.HasPrefix(step.Uses, "actions/cache@") {
			foundCache = true
			if step.With["path"] != "${{ steps.go-cache-path.outputs.path }}" {
				t.Fatal("cache must persist the actual GOCACHE path")
			}
			key, _ := step.With["key"].(string)
			for _, dimension := range []string{"runner.os", "runner.arch", "steps.setup-go.outputs.go-version", "hashFiles('go.sum')", "strategy.job-total", "matrix.shard", "github.head_ref || github.ref_name", "github.sha"} {
				if !strings.Contains(key, "${{ "+dimension+" }}") {
					t.Fatalf("cache key missing %s", dimension)
				}
			}
			restore, _ := step.With["restore-keys"].(string)
			prefix := strings.TrimSuffix(key, "${{ github.sha }}")
			mainPrefix := strings.Replace(prefix, "${{ github.head_ref || github.ref_name }}", "main", 1)
			if restore != prefix+"\n"+mainPrefix+"\n" {
				t.Fatal("cache must restore this branch before main without crossing platforms, toolchains, dependencies or shards")
			}
		}
		if strings.Contains(step.Run, "cli.mjs run go --full --timing") {
			t.Fatal("timing budgets run on routed hardware")
		}
		if strings.Contains(step.Run, "ci-go-shards test ") || strings.Contains(step.Run, "ci-go-shards needs-shell ") {
			if !strings.Contains(step.Run, `-count "$AEON_GO_SHARD_COUNT"`) {
				t.Fatal("command ignores selected count")
			}
		}
	}
	if !foundCache {
		t.Fatal("missing explicit rolling Go build cache")
	}
	if !foundFreshTests {
		t.Fatal("all shard layouts and events must execute fresh tests and reject cached success")
	}
}

// A child script is deliberately outside Go's test-result cache inputs. The
// workflow must still notice a failing script when only that script changes.
func TestWorkflowShardRerunsChangedChildScript(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string
				Env  map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	var flags string
	for _, step := range workflow.Jobs["go-test"].Steps {
		if strings.HasPrefix(step.Name, "Test this shard") {
			flags = step.Env["GOFLAGS"]
		}
	}
	for name, plan := range map[string]shardPlan{
		"whole":      {whole: []string{"./p"}},
		"named":      {runs: []namedRun{{path: "./p", names: []string{"TestChild"}}}},
		"catch-all":  {runs: []namedRun{{path: "./p", names: []string{"TestOther"}, skip: true}}},
		"empty-skip": {runs: []namedRun{{path: "./p", skip: true}}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "p"), 0o755); err != nil {
				t.Fatal(err)
			}
			for path, source := range map[string]string{
				"go.mod":   "module example.com/child\n\ngo 1.26.0\n",
				"child.sh": "exit 0\n",
				"p/child_test.go": `package p
import ("os/exec"; "testing")
func TestChild(t *testing.T) {
	if err := exec.Command("sh", "../child.sh").Run(); err != nil {
		t.Fatal("child script changed:", err)
	}
}
`,
			} {
				if err := os.WriteFile(filepath.Join(dir, path), []byte(source), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			commands, err := plan.commandArgs()
			if err != nil || len(commands) != 1 {
				t.Fatalf("commands=%v err=%v", commands, err)
			}
			run := func() ([]byte, error) {
				cmd := exec.Command("go", commands[0]...)
				cmd.Dir = dir
				cmd.Env = append(withoutEnvPrefix(os.Environ(), "GOFLAGS="), "GOFLAGS="+flags)
				return cmd.CombinedOutput()
			}
			if out, err := run(); err != nil {
				t.Fatalf("first execution: %v\n%s", err, out)
			}
			if err := os.WriteFile(filepath.Join(dir, "child.sh"), []byte("exit 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := run()
			if err == nil || !strings.Contains(string(out), "child script changed:") {
				t.Fatalf("changed child must execute and fail, err=%v\n%s", err, out)
			}
		})
	}
}

func TestWorkflowVolatileIdentityLint(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("workflow lint requires python3")
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct{ Name, Run string }
		}
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	var script string
	for _, step := range workflow.Jobs["go-static"].Steps {
		if step.Name == "Forbid volatile CI identity reads in Go tests" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("missing volatile identity lint in required static job")
	}
	check := func(t *testing.T, path, source string, wantOK bool) {
		t.Helper()
		dir := t.TempDir()
		file := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "-c", script)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if (err == nil) != wantOK {
			t.Fatalf("lint success=%t, want %t: %s", err == nil, wantOK, out)
		}
	}
	t.Run("stable-environment", func(t *testing.T) {
		check(t, "fixture_test.go", `package fixture; var value = os.Getenv("AEON_TEST_DATABASE_URL")`, true)
	})
	for _, suffix := range []string{"SHA", "RUN_ID", "RUN_NUMBER", "RUN_ATTEMPT"} {
		name := "GITHUB_" + suffix
		t.Run(suffix+"-multiline", func(t *testing.T) {
			check(t, "nested/fixture_test.go", "package fixture\nvar value = os.LookupEnv(\n\""+name+"\",\n)\n", false)
		})
		t.Run(suffix+"-constant", func(t *testing.T) {
			check(t, "fixture_test.go", "package fixture\nconst key = \""+name+"\"\nvar value = os.Getenv(key)\n", false)
		})
	}
	// The narrowly allowed release binding must not exempt other reads in
	// that file, or the same literal in any other test file.
	binding := `package fixture; var binding = "--source-digest \"$` + "GITHUB_" + `SHA\""`
	const releaseTest = "scripts/releaseworkflow/workflow_test.go"
	t.Run("release-contract", func(t *testing.T) {
		check(t, releaseTest, binding, true)
	})
	t.Run("release-file-read", func(t *testing.T) {
		check(t, releaseTest, binding+"\nvar value = os.Getenv(\"GITHUB_"+"SHA\")\n", false)
	})
	t.Run("other-file-binding", func(t *testing.T) {
		check(t, "fixture_test.go", binding, false)
	})
}

func TestParseFileRejectsBadShard(t *testing.T) {
	if _, err := parseFile("9 1 p\n", hostedShardCount); err == nil {
		t.Fatal("expected shard rejection")
	}
}

func TestCheckedInLayoutsHaveTheSameMeasuredInventory(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	var hosted map[string]int
	for _, count := range []int{hostedShardCount, macShardCount} {
		items, err := loadItems(root, "", count)
		if err != nil {
			t.Fatal(err)
		}
		if err := validate(items, sequentialBudgetMS, count); err != nil {
			t.Fatalf("%d-way layout: %v", count, err)
		}
		inventory := map[string]int{}
		for _, it := range items {
			inventory[it.key()] = it.MS
		}
		if hosted == nil {
			hosted = inventory
			continue
		}
		if len(inventory) != len(hosted) {
			t.Fatalf("four/seven inventory sizes differ: %d/%d", len(inventory), len(hosted))
		}
		for key, ms := range hosted {
			if got, ok := inventory[key]; !ok || got != ms {
				t.Fatalf("four-way inventory changes timing/item %q", key)
			}
		}
	}
}

func TestCountAndShardBounds(t *testing.T) {
	for _, count := range []int{0, -1, 1, 3, 5, 8} {
		if _, err := parseFile("1 1 p\n", count); err == nil {
			t.Fatalf("invalid count %d accepted", count)
		}
		for _, command := range []func([]string) error{cmdGenerate, cmdCheck, cmdTest, cmdPackages, cmdNeedsShell} {
			if err := command([]string{"-count", strconv.Itoa(count)}); err == nil {
				t.Fatalf("CLI accepted count %d", count)
			}
		}
	}
	if _, err := parseFile("5 1 p\n", macShardCount); err == nil {
		t.Fatal("four-way layout accepted shard five")
	}
	for _, count := range []int{macShardCount, hostedShardCount} {
		for _, shard := range []int{0, count + 1} {
			if _, err := planShard(nil, nil, shard, count); err == nil {
				t.Fatalf("count %d accepted shard %d", count, shard)
			}
			for _, command := range []func([]string) error{cmdTest, cmdPackages, cmdNeedsShell} {
				if err := command([]string{"-count", strconv.Itoa(count), "-shard", strconv.Itoa(shard)}); err == nil {
					t.Fatalf("CLI accepted count %d shard %d", count, shard)
				}
			}
		}
	}
}

func TestLightestShardPrefersLowerNumber(t *testing.T) {
	items := []Item{
		{Shard: 2, MS: 5, Path: "b"},
		{Shard: 1, MS: 5, Path: "a"},
	}
	if got := lightestShard(items, hostedShardCount); got != 1 {
		t.Fatalf("got %d", got)
	}
}

func TestAdditionsRunOnExactlyOneShard(t *testing.T) {
	const pkg = "p"
	items := []Item{
		{Shard: 1, MS: 1000, Path: pkg, Test: "TestKeep"},
		{Shard: 2, MS: 1, Path: pkg, Test: "TestOther"},
	}
	listed := []string{pkg, "q"}
	runnable := map[string][]string{
		pkg: {"TestKeep", "TestOther", "TestNew", "FuzzNew", "Example_new"},
	}
	if err := coverageHoles(items, listed, runnable, hostedShardCount); err != nil {
		t.Fatal(err)
	}
	notes := formatDrift(assignmentDrift(listed, items, runnable))
	for _, want := range []string{"rebalance recommended", "q", "TestNew", "FuzzNew", "Example_new", "::warning::rebalance recommended"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("drift note missing %q:\n%s", want, notes)
		}
	}
	matched := formatDrift(assignmentDrift([]string{pkg}, items, map[string][]string{pkg: {"TestKeep", "TestOther"}}))
	if matched != "" {
		t.Fatalf("balanced file was marked drifted: %s", matched)
	}
	owners := func(path, name string) []int {
		t.Helper()
		var got []int
		for shard := 1; shard <= hostedShardCount; shard++ {
			plan, err := planShard(items, listed, shard, hostedShardCount)
			if err != nil {
				t.Fatal(err)
			}
			if plan.runsTest(path, name) {
				got = append(got, shard)
			}
		}
		return got
	}
	for _, name := range []string{"TestKeep", "TestNew", "FuzzNew", "Example_new"} {
		if got := owners(pkg, name); len(got) != 1 || got[0] != 1 {
			t.Fatalf("%s runs on %v", name, got)
		}
	}
	if got := owners(pkg, "TestOther"); len(got) != 1 || got[0] != 2 {
		t.Fatalf("TestOther runs on %v", got)
	}
	if got := owners("q", "TestQ"); len(got) != 1 || got[0] != 2 {
		t.Fatalf("new package runs on %v", got)
	}
	catchAll, err := planShard(items, listed, 1, hostedShardCount)
	if err != nil {
		t.Fatal(err)
	}
	args, err := catchAll.commandArgs()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 || strings.Join(args[0], " ") != "test p -skip ^(TestOther)$" {
		t.Fatalf("catch-all args %q", args)
	}
	other, err := planShard(items, listed, 2, hostedShardCount)
	if err != nil {
		t.Fatal(err)
	}
	args, err = other.commandArgs()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || strings.Join(args[0], " ") != "test p -run ^(TestOther)$" || strings.Join(args[1], " ") != "test q" {
		t.Fatalf("other shard args %q", args)
	}
}

func TestCoverageHolesRejectsADroppedTest(t *testing.T) {
	err := coverageHoles(nil, nil, map[string][]string{"p": {"TestNew"}}, hostedShardCount)
	if err == nil {
		t.Fatal("dropped test was accepted")
	}
}

func TestGoTestRunsAdditionsOnce(t *testing.T) {
	for _, count := range []int{macShardCount, hostedShardCount} {
		t.Run(strconv.Itoa(count), func(t *testing.T) { runAdditionFixture(t, count) })
	}
}

func runAdditionFixture(t *testing.T, count int) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/shards\n\ngo 1.26.0\n")
	write("p/p_test.go", `package p

import (
	"fmt"
	"os"
	"testing"
)

func hit(name string) {
	f, err := os.OpenFile(os.Getenv("HITS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	if _, err := fmt.Fprintln(f, name); err != nil {
		panic(err)
	}
	if err := f.Close(); err != nil {
		panic(err)
	}
}

func TestKeep(t *testing.T) {
	hit("TestKeep")
	t.Run("sub", func(t *testing.T) { hit("TestKeep/sub") })
}

func TestOther(t *testing.T) {
	hit("TestOther")
	t.Run("sub", func(t *testing.T) { hit("TestOther/sub") })
}

func TestNew(t *testing.T) {
	hit("TestNew")
	t.Run("sub", func(t *testing.T) { hit("TestNew/sub") })
}

func FuzzNew(f *testing.F) {
	f.Add(1)
	f.Fuzz(func(t *testing.T, n int) { hit("FuzzNew") })
}

func Example_new() {
	hit("Example_new")
	fmt.Println("ok")
	// Output:
	// ok
}
`)
	write("q/q_test.go", `package q

import (
	"fmt"
	"os"
	"testing"
)

func hit(name string) {
	f, err := os.OpenFile(os.Getenv("HITS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	if _, err := fmt.Fprintln(f, name); err != nil {
		panic(err)
	}
	if err := f.Close(); err != nil {
		panic(err)
	}
}

func TestQ(t *testing.T) { hit("TestQ") }
`)
	const pkgP = "example.com/shards/p"
	const pkgQ = "example.com/shards/q"
	items := []Item{
		{Shard: 1, MS: 1000, Path: pkgP, Test: "TestKeep"},
		{Shard: 2, MS: 1, Path: pkgP, Test: "TestOther"},
	}
	listed := []string{pkgP, pkgQ}
	hits := filepath.Join(root, "hits")
	env := withoutEnvPrefix(os.Environ(), "HITS=")
	env = append(env, "HITS="+hits)
	for shard := 1; shard <= count; shard++ {
		plan, err := planShard(items, listed, shard, count)
		if err != nil {
			t.Fatal(err)
		}
		cmds, err := plan.commandArgs()
		if err != nil {
			t.Fatal(err)
		}
		for _, args := range cmds {
			cmd := exec.Command("go", append(args, "-count=1", "-timeout=60s")...)
			cmd.Dir = root
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("shard %d go %s: %v\n%s", shard, strings.Join(args, " "), err, out)
			}
		}
	}
	body, err := os.ReadFile(hits)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line != "" {
			got[line]++
		}
	}
	want := []string{"TestKeep", "TestKeep/sub", "TestOther", "TestOther/sub", "TestNew", "TestNew/sub", "FuzzNew", "Example_new", "TestQ"}
	for _, name := range want {
		if got[name] != 1 {
			t.Fatalf("%s ran %d times; all hits:\n%s", name, got[name], body)
		}
		delete(got, name)
	}
	if len(got) != 0 {
		t.Fatalf("unexpected hits %v", got)
	}
}

func TestTimingBudgetsLeaveTheParallelShards(t *testing.T) {
	items := []Item{
		{Shard: 1, MS: 50, Path: nodesPackage, Test: "TestKeep"},
		{Shard: 1, MS: 40, Path: nodesPackage, Test: "TestPlanningBulkUsagePerformance"},
		{Shard: 2, MS: 30, Path: nodesPackage, Test: "TestSafeListPerformancePlan"},
		{Shard: 4, MS: 20, Path: nodesPackage, Test: "TestList6000FiltersPerformance"},
		{Shard: 3, MS: 9, Path: "github.com/inspr-at/paimos/internal/filler3"},
		{Shard: 5, MS: 8, Path: "github.com/inspr-at/paimos/internal/filler5"},
		{Shard: 6, MS: 7, Path: "github.com/inspr-at/paimos/internal/filler6"},
		{Shard: 7, MS: 6, Path: "github.com/inspr-at/paimos/internal/filler7"},
	}
	listed := []string{
		nodesPackage,
		"github.com/inspr-at/paimos/internal/filler3",
		"github.com/inspr-at/paimos/internal/filler5",
		"github.com/inspr-at/paimos/internal/filler6",
		"github.com/inspr-at/paimos/internal/filler7",
	}
	runnable := map[string][]string{
		nodesPackage: {
			"TestKeep", "TestNew", "TestSafeListPerformancePlan",
			"TestPlanningBulkUsagePerformance", "TestList6000Performance", "TestList6000FiltersPerformance",
			"TestList6000PerformanceWithStaleKindStatistics",
		},
	}
	if err := coverageHoles(items, listed, runnable, hostedShardCount); err != nil {
		t.Fatal(err)
	}
	catch, err := planShard(items, listed, 1, hostedShardCount)
	if err != nil {
		t.Fatal(err)
	}
	args, err := catch.commandArgs()
	if err != nil {
		t.Fatal(err)
	}
	wantSkip := "test " + nodesPackage + " -skip ^(TestList6000FiltersPerformance|TestList6000Performance|TestList6000PerformanceWithStaleKindStatistics|TestPlanningBulkUsagePerformance|TestSafeListPerformancePlan)$"
	if len(args) != 1 || strings.Join(args[0], " ") != wantSkip {
		t.Fatalf("catch-all args %q", args)
	}
	other, err := planShard(items, listed, 2, hostedShardCount)
	if err != nil {
		t.Fatal(err)
	}
	args, err = other.commandArgs()
	if err != nil {
		t.Fatal(err)
	}
	wantRun := "test " + nodesPackage + " -run ^(TestSafeListPerformancePlan)$"
	if len(args) != 1 || strings.Join(args[0], " ") != wantRun {
		t.Fatalf("shard 2 args %q", args)
	}
	for shard := 1; shard <= hostedShardCount; shard++ {
		plan, err := planShard(items, listed, shard, hostedShardCount)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range timingBudgetNames(nodesPackage) {
			if plan.runsTest(nodesPackage, name) {
				t.Fatalf("shard %d runs %s", shard, name)
			}
		}
	}
	serial := timingShardPlan(listed)
	timed, err := serial.timingCommandArgs()
	if err != nil {
		t.Fatal(err)
	}
	wantTimed := "test -p 1 " + nodesPackage + " -run ^(TestList6000FiltersPerformance|TestList6000Performance|TestList6000PerformanceWithStaleKindStatistics|TestPlanningBulkUsagePerformance)$"
	if strings.Join(timed, " ") != wantTimed {
		t.Fatalf("timing args %q", timed)
	}
	for _, name := range runnable[nodesPackage] {
		onSerial := serial.runsTest(nodesPackage, name)
		if isTimingBudget(nodesPackage, name) != onSerial {
			t.Fatalf("%s serial=%t", name, onSerial)
		}
	}
}

func TestWholePackageSkipsTimingBudgets(t *testing.T) {
	items := []Item{
		{Shard: 1, MS: 10, Path: nodesPackage},
		{Shard: 2, MS: 9, Path: "b"},
		{Shard: 3, MS: 8, Path: "c"},
		{Shard: 4, MS: 7, Path: "d"},
		{Shard: 5, MS: 6, Path: "e"},
		{Shard: 6, MS: 5, Path: "f"},
		{Shard: 7, MS: 4, Path: "g"},
	}
	listed := []string{nodesPackage, "b", "c", "d", "e", "f", "g"}
	if err := coverageHoles(items, listed, nil, hostedShardCount); err != nil {
		t.Fatal(err)
	}
	plan, err := planShard(items, listed, 1, hostedShardCount)
	if err != nil {
		t.Fatal(err)
	}
	args, err := plan.commandArgs()
	if err != nil {
		t.Fatal(err)
	}
	want := "test " + nodesPackage + " -skip ^(TestList6000FiltersPerformance|TestList6000Performance|TestList6000PerformanceWithStaleKindStatistics|TestPlanningBulkUsagePerformance)$"
	if len(args) != 1 || strings.Join(args[0], " ") != want {
		t.Fatalf("%q", args)
	}
	if !plan.runsTest(nodesPackage, "TestKeep") || plan.runsTest(nodesPackage, "TestList6000Performance") {
		t.Fatal("whole package skip did not keep ordinary tests and drop the budget")
	}
}

func TestClassifyTimedTests(t *testing.T) {
	found := map[string][]string{
		nodesPackage: {
			"TestList6000FiltersPerformance",
			"TestList6000Performance",
			"TestList6000PerformanceWithStaleKindStatistics",
			"TestPlanningBulkUsagePerformance",
			"TestSafeListPerformancePlan",
		},
	}
	if err := unclassifiedPerformance(found); err != nil {
		t.Fatal(err)
	}
	extra := map[string][]string{
		nodesPackage: append(append([]string{}, found[nodesPackage]...), "TestExtraPerformance"),
	}
	if err := unclassifiedPerformance(extra); err == nil {
		t.Fatal("extra Performance test was accepted")
	}
	missing := map[string][]string{nodesPackage: {"TestSafeListPerformancePlan"}}
	if err := unclassifiedPerformance(missing); err == nil {
		t.Fatal("missing timing budget was accepted")
	}
}

func TestTimingStepUsesHostedAndIsRequiredForFullValidation(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			RunsOn string `yaml:"runs-on"`
			If     string `yaml:"if"`
			Needs  any    `yaml:"needs"`
			Steps  []struct {
				Run string            `yaml:"run"`
				If  string            `yaml:"if"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	timing := workflow.Jobs["go-timing"]
	if timing.RunsOn != "ubuntu-latest" || timing.If != "always() && needs.ci-plan.result == 'success' && (needs.ci-plan.outputs.lane == 'full') && needs.tree-reuse.outputs.reuse != 'merge_group'" || !reflect.DeepEqual(timing.Needs, []any{"ci-plan", "tree-reuse"}) {
		t.Fatal("timing job must run hosted for full validation")
	}
	count := 0
	for _, step := range timing.Steps {
		if strings.Contains(step.Run, "cli.mjs run go --full --timing") {
			count++
			if step.If != "" {
				t.Fatal("timing budgets have a conditional skip")
			}
			if step.Env["GOFLAGS"] != "-count=1" {
				t.Fatal("timing budgets must execute rather than replay cached results")
			}
		}
	}
	if count != 1 {
		t.Fatalf("timing command runs %d times", count)
	}
	foundGuard := false
	for _, step := range workflow.Jobs["release-check-run"].Steps {
		if strings.Contains(step.Run, "go test ./scripts/ci-runner-guard -count=1") {
			foundGuard = true
		}
	}
	if !foundGuard {
		t.Fatal("release runner guard tests must bypass cached results")
	}
	gate := workflow.Jobs["go"]
	needs, ok := gate.Needs.([]any)
	hasTiming := false
	for _, need := range needs {
		if need == "go-timing" {
			hasTiming = true
		}
	}
	if gate.If != "always()" || !ok || !hasTiming ||
		!strings.Contains(gate.Steps[0].Run, `full) expected=success`) ||
		!strings.Contains(gate.Steps[0].Run, `test "${GO_TIMING}" = "$expected"`) {
		t.Fatal("required go gate does not require successful timing budgets")
	}
}

func TestTimingBudgetsRunOnceInGo(t *testing.T) {
	for _, count := range []int{macShardCount, hostedShardCount} {
		t.Run(strconv.Itoa(count), func(t *testing.T) { runTimingFixture(t, count) })
	}
}

func runTimingFixture(t *testing.T, count int) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module github.com/inspr-at/paimos\n\ngo 1.26.0\n")
	write("internal/nodes/budget_test.go", `package nodes

import (
	"fmt"
	"os"
	"testing"
)

func hit(name string) {
	f, err := os.OpenFile(os.Getenv("HITS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	if _, err := fmt.Fprintln(f, name); err != nil {
		panic(err)
	}
	if err := f.Close(); err != nil {
		panic(err)
	}
}

func TestKeep(t *testing.T) { hit("TestKeep") }

func TestNew(t *testing.T) { hit("TestNew") }

func TestSafeListPerformancePlan(t *testing.T) { hit("TestSafeListPerformancePlan") }

func TestPlanningBulkUsagePerformance(t *testing.T) {
	hit("TestPlanningBulkUsagePerformance")
	t.Run("sub", func(t *testing.T) { hit("TestPlanningBulkUsagePerformance/sub") })
}

func TestList6000Performance(t *testing.T) { hit("TestList6000Performance") }

func TestList6000PerformanceWithStaleKindStatistics(t *testing.T) {
	if os.Getenv("AEON_SHARD_FIXTURE_TIMING") != "1" {
		t.Fatal("stale-kind timing budget ran on a parallel shard")
	}
	hit("TestList6000PerformanceWithStaleKindStatistics")
}

func TestList6000FiltersPerformance(t *testing.T) { hit("TestList6000FiltersPerformance") }
`)
	items := []Item{
		{Shard: 1, MS: 50, Path: nodesPackage, Test: "TestKeep"},
		{Shard: 1, MS: 40, Path: nodesPackage, Test: "TestPlanningBulkUsagePerformance"},
		{Shard: 2, MS: 30, Path: nodesPackage, Test: "TestSafeListPerformancePlan"},
		{Shard: 4, MS: 20, Path: nodesPackage, Test: "TestList6000FiltersPerformance"},
		{Shard: 3, MS: 9, Path: "github.com/inspr-at/paimos/internal/filler3"},
		{Shard: 5, MS: 8, Path: "github.com/inspr-at/paimos/internal/filler5"},
		{Shard: 6, MS: 7, Path: "github.com/inspr-at/paimos/internal/filler6"},
		{Shard: 7, MS: 6, Path: "github.com/inspr-at/paimos/internal/filler7"},
	}
	if count == macShardCount {
		var selected []Item
		for _, it := range items {
			if it.Shard <= count {
				selected = append(selected, it)
			}
		}
		items = selected
	}
	listed := []string{nodesPackage}
	hits := filepath.Join(root, "hits")
	env := withoutEnvPrefix(os.Environ(), "HITS=")
	env = withoutEnvPrefix(env, "AEON_SHARD_FIXTURE_TIMING=")
	env = append(env, "HITS="+hits)
	ran := 0
	for shard := 1; shard <= count; shard++ {
		plan, err := planShard(items, listed, shard, count)
		if err != nil {
			t.Fatal(err)
		}
		cmds, err := plan.commandArgs()
		if err != nil {
			t.Fatal(err)
		}
		for _, args := range cmds {
			if !strings.Contains(strings.Join(args, " "), nodesPackage) {
				continue
			}
			ran++
			cmd := exec.Command("go", append(args, "-count=1", "-timeout=60s")...)
			cmd.Dir = root
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("shard %d go %s: %v\n%s", shard, strings.Join(args, " "), err, out)
			}
		}
	}
	if ran != 2 {
		t.Fatalf("ran %d nodes commands, want the catch-all and the exempt test", ran)
	}
	timed, err := timingShardPlan(listed).timingCommandArgs()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", append(timed, "-count=1", "-timeout=60s")...)
	cmd.Dir = root
	cmd.Env = append(env, "AEON_SHARD_FIXTURE_TIMING=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("timing go %s: %v\n%s", strings.Join(timed, " "), err, out)
	}
	body, err := os.ReadFile(hits)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line != "" {
			got[line]++
		}
	}
	want := []string{
		"TestKeep", "TestNew", "TestSafeListPerformancePlan",
		"TestPlanningBulkUsagePerformance", "TestPlanningBulkUsagePerformance/sub",
		"TestList6000Performance", "TestList6000FiltersPerformance",
		"TestList6000PerformanceWithStaleKindStatistics",
	}
	for _, name := range want {
		if got[name] != 1 {
			t.Fatalf("%s ran %d times; hits:\n%s", name, got[name], body)
		}
		delete(got, name)
	}
	if len(got) != 0 {
		t.Fatalf("unexpected hits %v", got)
	}
}

func TestListTimedTestNames(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/scan\n\ngo 1.26.0\n")
	write("p/p_test.go", `package p

import "testing"

func TestKeep(t *testing.T) {}
func TestExtraPerformance(t *testing.T) {}
`)
	got, err := listPerformanceTests(root, hostedShardCount)
	if err != nil {
		t.Fatal(err)
	}
	names := got["example.com/scan/p"]
	if len(got) != 1 || len(names) != 1 || names[0] != "TestExtraPerformance" {
		t.Fatalf("%v", got)
	}
}

func TestLayoutInventoryUsesTheRunnerArchitecture(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/platform\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		body := "package p\nimport \"testing\"\nfunc Test_" + arch + "(t *testing.T) {}\n"
		if err := os.WriteFile(filepath.Join(root, "p", "arch_"+arch+"_test.go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		count int
		arch  string
	}{{macShardCount, "arm64"}, {hostedShardCount, "amd64"}} {
		// Linux must list the tests this runner can execute even when a rerun
		// retains the other runner class's shard count. Local Darwin checks
		// still enumerate both intended CI architectures.
		if runtime.GOOS == "linux" {
			tc.arch = runtime.GOARCH
		}
		names, err := listRunnableTests(root, "example.com/platform/p", tc.count)
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 1 || names[0] != "Test_"+tc.arch {
			t.Fatalf("count %d lists %v, want %s", tc.count, names, tc.arch)
		}
	}
}

func withoutEnvPrefix(env []string, prefix string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			continue
		}
		out = append(out, e)
	}
	return out
}
