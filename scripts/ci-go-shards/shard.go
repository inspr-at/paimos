// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const shardCount = 4

// splitAboveMS is the CI package elapsed time past which one test binary
// cannot finish inside the four-minute gate, so generate splits that package
// by test name. internal/harness and internal/nodes are the measured cases.
const splitAboveMS = 140_000

// sequentialBudgetMS is the most time one test process on a shard may carry.
// Postgres init, checkout and the aggregate gate sit outside this number,
// and together they still have to leave the go check under four minutes.
const sequentialBudgetMS = 150_000

// Item is one balanced unit: a whole package, or one test of a split package.
type Item struct {
	Shard int
	MS    int
	Path  string
	Test  string
}

func (it Item) key() string {
	return it.Path + "\x00" + it.Test
}

var logResultRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z (ok|FAIL|\?)\s+(\S+)\s+(?:(\d+(?:\.\d+)?)s|\[no test files\])`)

// parseLog reads a GitHub Actions job log (or a raw go test log that includes
// the runner timestamp) and returns package elapsed milliseconds.
func parseLog(text string) (map[string]int, error) {
	times := map[string]int{}
	for _, line := range strings.Split(text, "\n") {
		m := logResultRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ms := 0
		if m[3] != "" {
			var err error
			ms, err = secondsFieldToMS(m[3])
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", m[2], err)
			}
		}
		if _, ok := times[m[2]]; ok {
			return nil, fmt.Errorf("duplicate package in log: %s", m[2])
		}
		times[m[2]] = ms
	}
	if len(times) == 0 {
		return nil, fmt.Errorf("log contained no go test package results")
	}
	return times, nil
}

func secondsFieldToMS(s string) (int, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("bad seconds %q", s)
	}
	return int(math.Round(f * 1000)), nil
}

type testEvent struct {
	Action  string
	Package string
	Test    string
	Elapsed float64
}

// parseTestJSON reads go test -json lines and returns top-level test times.
// Subtests are omitted because the parent elapsed already includes them.
func parseTestJSON(r io.Reader) ([]Item, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	seen := map[string]Item{}
	var order []string
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev testEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("test json: %w", err)
		}
		if ev.Test == "" || strings.Contains(ev.Test, "/") {
			continue
		}
		switch ev.Action {
		case "pass", "fail", "skip":
		default:
			continue
		}
		if ev.Package == "" {
			return nil, fmt.Errorf("test %s has no package", ev.Test)
		}
		ms := int(math.Round(ev.Elapsed * 1000))
		if ms < 0 {
			return nil, fmt.Errorf("negative elapsed for %s", ev.Test)
		}
		it := Item{MS: ms, Path: ev.Package, Test: ev.Test}
		if _, ok := seen[it.key()]; ok {
			return nil, fmt.Errorf("duplicate test result %s %s", ev.Package, ev.Test)
		}
		seen[it.key()] = it
		order = append(order, it.key())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(order))
	for _, k := range order {
		out = append(out, seen[k])
	}
	return out, nil
}

// buildItems expands packages whose CI time is over splitAbove into per-test
// items. Test times are scaled so each split package still sums to its CI time.
func buildItems(pkgMS map[string]int, tests []Item, splitAbove int) ([]Item, error) {
	byPkg := map[string][]Item{}
	for _, it := range tests {
		if it.Test == "" || it.Path == "" {
			return nil, fmt.Errorf("test row missing path or name")
		}
		byPkg[it.Path] = append(byPkg[it.Path], it)
	}
	var items []Item
	var paths []string
	for path := range pkgMS {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		ms := pkgMS[path]
		rows := byPkg[path]
		if ms > splitAbove && len(rows) == 0 {
			return nil, fmt.Errorf("%s is %dms, over %dms, and has no per-test timings", path, ms, splitAbove)
		}
		if ms <= splitAbove {
			items = append(items, Item{MS: ms, Path: path})
			continue
		}
		scaled, err := scaleTo(rows, ms)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		items = append(items, scaled...)
	}
	return items, nil
}

// scaleTo rescales local millisecond measurements so they sum to target.
func scaleTo(local []Item, target int) ([]Item, error) {
	if len(local) == 0 {
		return nil, fmt.Errorf("no tests to scale")
	}
	if target < 0 {
		return nil, fmt.Errorf("negative target")
	}
	out := append([]Item(nil), local...)
	sum := 0
	for _, it := range out {
		if it.MS < 0 {
			return nil, fmt.Errorf("negative time for %s", it.Test)
		}
		sum += it.MS
	}
	if sum == 0 {
		each := target / len(out)
		used := 0
		for i := range out {
			out[i].MS = each
			used += each
		}
		out[0].MS += target - used
		return out, nil
	}
	used := 0
	maxI := 0
	for i := range out {
		out[i].MS = int(int64(out[i].MS) * int64(target) / int64(sum))
		used += out[i].MS
		if out[i].MS > out[maxI].MS {
			maxI = i
		}
	}
	out[maxI].MS += target - used
	return out, nil
}

// balance assigns items to shards with longest-processing-time first.
// Equal load prefers the shard with fewer items, then the lower shard number.
func balance(items []Item, shards int) ([]Item, error) {
	if shards < 1 {
		return nil, fmt.Errorf("shard count must be positive")
	}
	if len(items) < shards {
		return nil, fmt.Errorf("%d items cannot fill %d shards", len(items), shards)
	}
	out := append([]Item(nil), items...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].MS != out[j].MS {
			return out[i].MS > out[j].MS
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Test < out[j].Test
	})
	type load struct{ ms, n int }
	loads := make([]load, shards)
	for i := range out {
		best := 0
		for s := 1; s < shards; s++ {
			if loads[s].ms < loads[best].ms ||
				(loads[s].ms == loads[best].ms && loads[s].n < loads[best].n) ||
				(loads[s].ms == loads[best].ms && loads[s].n == loads[best].n && s < best) {
				best = s
			}
		}
		out[i].Shard = best + 1
		loads[best].ms += out[i].MS
		loads[best].n++
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Shard != out[j].Shard {
			return out[i].Shard < out[j].Shard
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Test < out[j].Test
	})
	return out, nil
}

func formatFile(items []Item, splitAbove int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# AEON-408 CI Go shards. Regenerate with: go run ./scripts/ci-go-shards generate -log <job.log> -json <tests.json>\n")
	fmt.Fprintf(&b, "# Package times are go test elapsed milliseconds from ubuntu-latest job 109673368507 (run 36647374249, cbcab384).\n")
	fmt.Fprintf(&b, "# Packages over %dms are split by test. Per-test times were measured locally and scaled back to that package's CI elapsed time.\n", splitAbove)
	fmt.Fprintf(&b, "# A package added after that run is listed at 0ms until the next measurement.\n")
	fmt.Fprintf(&b, "# Columns: shard milliseconds import-path [TestName]\n")
	for _, it := range items {
		if it.Test == "" {
			fmt.Fprintf(&b, "%d %d %s\n", it.Shard, it.MS, it.Path)
			continue
		}
		fmt.Fprintf(&b, "%d %d %s %s\n", it.Shard, it.MS, it.Path, it.Test)
	}
	return b.String()
}

func parseFile(text string) ([]Item, error) {
	var items []Item
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 && len(f) != 4 {
			return nil, fmt.Errorf("line %d: want 3 or 4 fields", i+1)
		}
		shard, err := strconv.Atoi(f[0])
		if err != nil || shard < 1 || shard > shardCount {
			return nil, fmt.Errorf("line %d: shard must be 1..%d", i+1, shardCount)
		}
		ms, err := strconv.Atoi(f[1])
		if err != nil || ms < 0 {
			return nil, fmt.Errorf("line %d: milliseconds must be a non-negative integer", i+1)
		}
		it := Item{Shard: shard, MS: ms, Path: f[2]}
		if len(f) == 4 {
			it.Test = f[3]
		}
		items = append(items, it)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("shard file is empty")
	}
	return items, nil
}

// validate checks shape, longest-processing-time balance and the sequential budget.
func validate(items []Item, budgetMS int) error {
	if err := validateShape(items); err != nil {
		return err
	}
	cleared := append([]Item(nil), items...)
	for i := range cleared {
		cleared[i].Shard = 0
	}
	want, err := balance(cleared, shardCount)
	if err != nil {
		return err
	}
	got := map[string]int{}
	for _, it := range items {
		got[it.key()] = it.Shard
	}
	for _, it := range want {
		if got[it.key()] != it.Shard {
			return fmt.Errorf("shard assignment is not longest-processing-time balanced at %s %s (file shard %d, balanced shard %d)", it.Path, it.Test, got[it.key()], it.Shard)
		}
	}
	seq := sequentialByShard(items)
	for shard := 1; shard <= shardCount; shard++ {
		for path, ms := range seq[shard] {
			if ms > budgetMS {
				return fmt.Errorf("shard %d runs %s for %dms, over the %dms sequential budget", shard, path, ms, budgetMS)
			}
		}
	}
	return nil
}

func validateShape(items []Item) error {
	seen := map[string]Item{}
	whole := map[string]bool{}
	split := map[string]bool{}
	used := map[int]bool{}
	for _, it := range items {
		if it.Path == "" || strings.Contains(it.Path, " ") {
			return fmt.Errorf("bad import path %q", it.Path)
		}
		if _, ok := seen[it.key()]; ok {
			return fmt.Errorf("duplicate row %s %s", it.Path, it.Test)
		}
		seen[it.key()] = it
		used[it.Shard] = true
		if it.Test == "" {
			whole[it.Path] = true
		} else {
			split[it.Path] = true
		}
	}
	for path := range whole {
		if split[path] {
			return fmt.Errorf("%s is listed both as a whole package and as individual tests", path)
		}
	}
	for shard := 1; shard <= shardCount; shard++ {
		if !used[shard] {
			return fmt.Errorf("shard %d has no packages", shard)
		}
	}
	return nil
}

// sequentialByShard sums time per package on a shard. Tests of one package
// share a process, so that sum is a lower bound on the process.
func sequentialByShard(items []Item) map[int]map[string]int {
	out := map[int]map[string]int{}
	for _, it := range items {
		if out[it.Shard] == nil {
			out[it.Shard] = map[string]int{}
		}
		out[it.Shard][it.Path] += it.MS
	}
	return out
}

func shardStats(items []Item) string {
	seq := sequentialByShard(items)
	var b strings.Builder
	for shard := 1; shard <= shardCount; shard++ {
		sum := 0
		pkgs := len(seq[shard])
		maxMS := 0
		maxPath := ""
		for path, ms := range seq[shard] {
			sum += ms
			if ms > maxMS || (ms == maxMS && (maxPath == "" || path < maxPath)) {
				maxMS = ms
				maxPath = path
			}
		}
		fmt.Fprintf(&b, "shard %d sum=%dms packages=%d max_sequential=%dms %s\n", shard, sum, pkgs, maxMS, maxPath)
	}
	return b.String()
}

func runRegex(names []string) (string, error) {
	if len(names) == 0 {
		return "", fmt.Errorf("empty test list")
	}
	cp := append([]string(nil), names...)
	sort.Strings(cp)
	parts := make([]string, len(cp))
	for i, name := range cp {
		if !validTestName(name) {
			return "", fmt.Errorf("refusing test name %q", name)
		}
		parts[i] = regexp.QuoteMeta(name)
	}
	return "^(" + strings.Join(parts, "|") + ")$", nil
}

func validTestName(name string) bool {
	if !isRunnableTest(name) {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// isRunnableTest reports whether go test would run name. Benchmarks and fuzz
// targets are omitted because go test ./... does not run them.
func isRunnableTest(name string) bool {
	if name == "TestMain" {
		return false
	}
	for _, prefix := range []string{"Test", "Example"} {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := name[len(prefix):]
		if rest == "" {
			return prefix == "Example"
		}
		r, _ := utf8.DecodeRuneInString(rest)
		return !unicode.IsLower(r)
	}
	return false
}

// testNamesIn parses top-level Test and Example functions from one file.
func testNamesIn(filename string, src []byte) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, 0)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name == nil {
			continue
		}
		if isRunnableTest(fn.Name.Name) {
			names = append(names, fn.Name.Name)
		}
	}
	return names, nil
}
