// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
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
	assigned, err := balance(items, 4)
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
	parsed[0].Shard = parsed[0].Shard%4 + 1
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
`)
	got, err := testNamesIn("p_test.go", src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"TestA", "Example", "Example_foo"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v", got)
	}
}

func TestParseFileRejectsBadShard(t *testing.T) {
	if _, err := parseFile("5 1 p\n"); err == nil {
		t.Fatal("expected shard rejection")
	}
}
