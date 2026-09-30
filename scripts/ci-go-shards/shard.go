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

// shardCount is seven because five shards peaked at 232s and six shards, with
// every package over 90s split into its own process, peaked at 254s.
const shardCount = 7

const pairingPackage = "github.com/inspr-at/paimos/internal/agentpairing"
const pathProofTest = "TestPathProofCommandsRunInShells"

// shardNeedsShell reports whether shard runs the pairing path-proof test.
// A whole-package row runs every test, including that one.
func shardNeedsShell(items []Item, shard int) bool {
	for _, it := range items {
		if it.Shard != shard || it.Path != pairingPackage {
			continue
		}
		if it.Test == "" || it.Test == pathProofTest {
			return true
		}
	}
	return false
}

// splitAboveMS is the CI package elapsed past which one test binary cannot
// finish inside the hosted gate. Splitting a shorter package starts another
// process beside the others, and run 36698280679 showed that extra processes
// make the runner miss its own timing budgets. Inbox joined harness, nodes,
// and agentpairing above this line.
const splitAboveMS = 140_000

// sequentialBudgetMS is the most time one test process on a shard may carry.
// Postgres init, checkout and the aggregate gate sit outside this number,
// and together they still have to leave the shard under three and a half minutes.
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
	fmt.Fprintf(&b, "# Whole-package times are go test elapsed milliseconds from ubuntu-latest run 36695656920.\n")
	fmt.Fprintf(&b, "# Packages over %dms are split by test. Harness, nodes, and agentpairing keep their five-shard proportions.\n", splitAbove)
	fmt.Fprintf(&b, "# Inbox was measured locally and scaled to that run. Pairing tests added in release 14 were measured locally and scaled by the same ratio as the rest of that package.\n")
	fmt.Fprintf(&b, "# A package added after that run is listed at 0ms until the next measurement.\n")
	fmt.Fprintf(&b, "# A package absent from this file runs on the lightest shard. A split package's lowest shard skips tests assigned elsewhere, so a new Test, Example, or Fuzz still runs.\n")
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

// isRunnableTest reports whether ordinary go test runs a function of this name.
// The rule matches cmd/go's isTest for Test, Example, and Fuzz: the remainder
// is empty or does not start with a lower-case letter. Fuzz targets run their
// seed corpus without -fuzz. Benchmarks do not run unless -bench is set, and
// CI never passes -bench. TestMain is the process hook, not a test.
func isRunnableTest(name string) bool {
	if name == "TestMain" {
		return false
	}
	for _, prefix := range []string{"Test", "Example", "Fuzz"} {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := name[len(prefix):]
		if rest == "" {
			return true
		}
		r, _ := utf8.DecodeRuneInString(rest)
		return !unicode.IsLower(r)
	}
	return false
}

// testNamesIn parses top-level Test, Example, and Fuzz functions from one file.
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

// namedRun is one split package on a shard. skip selects -skip; otherwise -run.
// names are exact top-level tests. The regex has no slash, so a subtest follows
// its parent: -run keeps the parent's descendants and -skip drops them.
type namedRun struct {
	path  string
	skip  bool
	names []string
}

// shardPlan is everything one shard executes.
type shardPlan struct {
	whole []string
	runs  []namedRun
}

func (p shardPlan) empty() bool {
	return len(p.whole) == 0 && len(p.runs) == 0
}

func (p shardPlan) packages() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(path string) {
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	for _, path := range p.whole {
		add(path)
	}
	for _, run := range p.runs {
		add(run.path)
	}
	sort.Strings(out)
	return out
}

func (p shardPlan) commandArgs() ([][]string, error) {
	var out [][]string
	for _, run := range p.runs {
		if run.skip && len(run.names) == 0 {
			out = append(out, []string{"test", run.path})
			continue
		}
		re, err := runRegex(run.names)
		if err != nil {
			return nil, err
		}
		flagName := "-run"
		if run.skip {
			flagName = "-skip"
		}
		out = append(out, []string{"test", run.path, flagName, re})
	}
	if len(p.whole) > 0 {
		args := make([]string, 0, 1+len(p.whole))
		args = append(args, "test")
		args = append(args, p.whole...)
		out = append(out, args)
	}
	return out, nil
}

func (p shardPlan) runsPackage(path string) bool {
	for _, whole := range p.whole {
		if whole == path {
			return true
		}
	}
	for _, run := range p.runs {
		if run.path == path {
			return true
		}
	}
	return false
}

// runsTest reports whether this plan executes one top-level test.
// A whole package runs every test. A catch-all runs every name it does not skip.
func (p shardPlan) runsTest(path, name string) bool {
	for _, whole := range p.whole {
		if whole == path {
			return true
		}
	}
	for _, run := range p.runs {
		if run.path != path {
			continue
		}
		found := false
		for _, n := range run.names {
			if n == name {
				found = true
				break
			}
		}
		if run.skip {
			return !found
		}
		return found
	}
	return false
}

// lightestShard is the committed shard with the smallest total milliseconds.
// Ties take the lower shard number. A package missing from the file runs there.
func lightestShard(items []Item) int {
	sum := map[int]int{}
	for _, it := range items {
		if it.Shard < 1 || it.Shard > shardCount {
			continue
		}
		sum[it.Shard] += it.MS
	}
	best := 0
	bestMS := 0
	for shard, ms := range sum {
		if best == 0 || ms < bestMS || (ms == bestMS && shard < best) {
			best = shard
			bestMS = ms
		}
	}
	if best == 0 {
		return 1
	}
	return best
}

// planShard builds one shard's commands from the committed file plus the
// packages go list currently returns. The lowest shard of a split package is
// the catch-all: it skips only the names assigned to other shards, so a new
// Test, Example, or Fuzz runs there and nowhere else.
func planShard(items []Item, listed []string, shard int) (shardPlan, error) {
	if shard < 1 || shard > shardCount {
		return shardPlan{}, fmt.Errorf("shard must be 1..%d", shardCount)
	}
	assigned := map[string]struct{}{}
	for _, it := range items {
		assigned[it.Path] = struct{}{}
	}
	wholeSet := map[string]struct{}{}
	for _, it := range items {
		if it.Shard == shard && it.Test == "" {
			wholeSet[it.Path] = struct{}{}
		}
	}
	if shard == lightestShard(items) {
		for _, path := range listed {
			if _, ok := assigned[path]; ok {
				continue
			}
			wholeSet[path] = struct{}{}
		}
	}
	whole := make([]string, 0, len(wholeSet))
	for path := range wholeSet {
		whole = append(whole, path)
	}
	sort.Strings(whole)

	type group struct {
		catch        int
		mine, others []string
	}
	groups := map[string]*group{}
	for _, it := range items {
		if it.Test == "" {
			continue
		}
		g, ok := groups[it.Path]
		if !ok {
			g = &group{catch: it.Shard}
			groups[it.Path] = g
		}
		if it.Shard < g.catch {
			g.catch = it.Shard
		}
		if it.Shard == shard {
			g.mine = append(g.mine, it.Test)
		} else {
			g.others = append(g.others, it.Test)
		}
	}
	paths := make([]string, 0, len(groups))
	for path := range groups {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var runs []namedRun
	for _, path := range paths {
		g := groups[path]
		if g.catch == shard {
			runs = append(runs, namedRun{path: path, skip: true, names: append([]string(nil), g.others...)})
			continue
		}
		if len(g.mine) == 0 {
			continue
		}
		runs = append(runs, namedRun{path: path, names: append([]string(nil), g.mine...)})
	}
	return shardPlan{whole: whole, runs: runs}, nil
}

// coverageHoles fails when a listed package or a runnable split-package test
// would not run on exactly one shard. Drift of the committed file is separate
// and stays advisory.
func coverageHoles(items []Item, listed []string, runnable map[string][]string) error {
	split := map[string]bool{}
	for _, it := range items {
		if it.Test != "" {
			split[it.Path] = true
		}
	}
	plans := make([]shardPlan, 0, shardCount)
	for shard := 1; shard <= shardCount; shard++ {
		plan, err := planShard(items, listed, shard)
		if err != nil {
			return err
		}
		plans = append(plans, plan)
	}
	var holes []string
	for _, path := range listed {
		if split[path] {
			continue
		}
		n := 0
		for _, plan := range plans {
			if plan.runsPackage(path) {
				n++
			}
		}
		if n != 1 {
			holes = append(holes, fmt.Sprintf("%s runs on %d shards", path, n))
		}
	}
	paths := make([]string, 0, len(runnable))
	for path := range runnable {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		names := append([]string(nil), runnable[path]...)
		sort.Strings(names)
		for _, name := range names {
			n := 0
			for _, plan := range plans {
				if plan.runsTest(path, name) {
					n++
				}
			}
			if n != 1 {
				holes = append(holes, fmt.Sprintf("%s %s runs on %d shards", path, name, n))
			}
		}
	}
	if len(holes) == 0 {
		return nil
	}
	return fmt.Errorf("shard plan drops or duplicates work:\n%s", strings.Join(limit(holes, 12), "\n"))
}

// assignmentDrift compares the committed file with the packages and tests that
// exist now. The caller prints it as a rebalance hint and still runs the work.
func assignmentDrift(listed []string, items []Item, runnable map[string][]string) []string {
	inFile := map[string]struct{}{}
	split := map[string][]string{}
	for _, it := range items {
		inFile[it.Path] = struct{}{}
		if it.Test != "" {
			split[it.Path] = append(split[it.Path], it.Test)
		}
	}
	inList := map[string]struct{}{}
	for _, path := range listed {
		inList[path] = struct{}{}
	}
	var notes []string
	if err := samePathSet(listed, inFile); err != nil {
		notes = append(notes, err.Error())
	}
	paths := make([]string, 0, len(split))
	for path := range split {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if _, ok := inList[path]; !ok {
			continue
		}
		if err := sameNameSet(path, runnable[path], split[path]); err != nil {
			notes = append(notes, err.Error())
		}
	}
	return notes
}

func formatDrift(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("rebalance recommended\n")
	for _, note := range notes {
		b.WriteString(note)
		b.WriteByte('\n')
	}
	b.WriteString("::warning::rebalance recommended\n")
	return b.String()
}
