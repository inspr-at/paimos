// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
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
	assigned, err := balance(items, shardCount)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseFile(formatFile(assigned, 5))
	if err != nil {
		t.Fatal(err)
	}
	if err := validate(parsed, 100); err != nil {
		t.Fatal(err)
	}
	parsed[0].Shard = parsed[0].Shard%shardCount + 1
	if err := validate(parsed, 100); err == nil {
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

func TestWorkflowShardMatrixMatchesCount(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	nums := make([]string, shardCount)
	for i := 1; i <= shardCount; i++ {
		nums[i-1] = strconv.Itoa(i)
	}
	needle := "shard: [" + strings.Join(nums, ", ") + "]"
	if !strings.Contains(string(body), needle) {
		t.Fatalf("ci.yml missing %s", needle)
	}
}

func TestParseFileRejectsBadShard(t *testing.T) {
	if _, err := parseFile("9 1 p\n"); err == nil {
		t.Fatal("expected shard rejection")
	}
}

func TestLightestShardPrefersLowerNumber(t *testing.T) {
	items := []Item{
		{Shard: 2, MS: 5, Path: "b"},
		{Shard: 1, MS: 5, Path: "a"},
	}
	if got := lightestShard(items); got != 1 {
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
	if err := coverageHoles(items, listed, runnable); err != nil {
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
		for shard := 1; shard <= shardCount; shard++ {
			plan, err := planShard(items, listed, shard)
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
	catchAll, err := planShard(items, listed, 1)
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
	other, err := planShard(items, listed, 2)
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
	err := coverageHoles(nil, nil, map[string][]string{"p": {"TestNew"}})
	if err == nil {
		t.Fatal("dropped test was accepted")
	}
}

func TestGoTestRunsAdditionsOnce(t *testing.T) {
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
	for shard := 1; shard <= shardCount; shard++ {
		plan, err := planShard(items, listed, shard)
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
