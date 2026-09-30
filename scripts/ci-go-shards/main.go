// SPDX-License-Identifier: AGPL-3.0-only

// Command ci-go-shards keeps the CI go test package list balanced across five shards.
//
//	go run ./scripts/ci-go-shards generate -log job.log -json tests.json
//	go run ./scripts/ci-go-shards check
//	go run ./scripts/ci-go-shards test -shard 1
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "generate":
		err = cmdGenerate(os.Args[2:])
	case "check":
		err = cmdCheck(os.Args[2:])
	case "test":
		err = cmdTest(os.Args[2:])
	case "packages":
		err = cmdPackages(os.Args[2:])
	case "needs-shell":
		err = cmdNeedsShell(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		var code exitCode
		if errors.As(err, &code) {
			if msg := strings.TrimSpace(err.Error()); msg != "" && !strings.HasPrefix(msg, "exit ") {
				fmt.Fprintln(os.Stderr, msg)
			}
			os.Exit(int(code))
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func usage() {
	fmt.Fprintf(os.Stderr, `usage:
  ci-go-shards generate -log job.log -json tests.json [-out scripts/ci/go-shards.txt]
  ci-go-shards check [-file scripts/ci/go-shards.txt]
  ci-go-shards test -shard N [-file scripts/ci/go-shards.txt]
  ci-go-shards packages -shard N [-file scripts/ci/go-shards.txt]
  ci-go-shards needs-shell -shard N [-file scripts/ci/go-shards.txt]
`)
}

func cmdGenerate(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	logPath := fs.String("log", "", "GitHub Actions log (or go test output) with package elapsed times")
	jsonPath := fs.String("json", "", "go test -json output covering packages over the split threshold")
	outPath := fs.String("out", "", "shard file to write (default scripts/ci/go-shards.txt)")
	split := fs.Int("split-above", splitAboveMS, "split packages whose CI elapsed time exceeds this many milliseconds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *logPath == "" || *jsonPath == "" {
		return fmt.Errorf("generate requires -log and -json")
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	if *outPath == "" {
		*outPath = filepath.Join(root, "scripts/ci/go-shards.txt")
	}
	logText, err := os.ReadFile(*logPath)
	if err != nil {
		return err
	}
	logged, err := parseLog(string(logText))
	if err != nil {
		return err
	}
	listed, err := goList(root)
	if err != nil {
		return err
	}
	pkgMS, err := alignPackageTimes(listed, logged)
	if err != nil {
		return err
	}
	jsonFile, err := os.Open(*jsonPath)
	if err != nil {
		return err
	}
	defer jsonFile.Close()
	tests, err := parseTestJSON(jsonFile)
	if err != nil {
		return err
	}
	// Keep per-test rows only for packages that will be split, and only tests
	// the linux build actually runs.
	var splitPkgs []string
	for path, ms := range pkgMS {
		if ms > *split {
			splitPkgs = append(splitPkgs, path)
		}
	}
	sort.Strings(splitPkgs)
	measured := map[string]Item{}
	for _, it := range tests {
		measured[it.key()] = it
	}
	var used []Item
	for _, path := range splitPkgs {
		names, err := listRunnableTests(root, path)
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return fmt.Errorf("%s is over the split threshold but lists no tests", path)
		}
		for _, name := range names {
			it, ok := measured[Item{Path: path, Test: name}.key()]
			if !ok {
				return fmt.Errorf("%s %s has no per-test timing", path, name)
			}
			used = append(used, it)
		}
	}
	items, err := buildItems(pkgMS, used, *split)
	if err != nil {
		return err
	}
	assigned, err := balance(items, shardCount)
	if err != nil {
		return err
	}
	if err := validate(assigned, sequentialBudgetMS); err != nil {
		return err
	}
	body := formatFile(assigned, *split)
	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(*outPath, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Fprint(os.Stdout, shardStats(assigned))
	return nil
}

func cmdCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	file := fs.String("file", "", "shard file (default scripts/ci/go-shards.txt)")
	budget := fs.Int("budget", sequentialBudgetMS, "maximum sequential milliseconds of one package on one shard")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	items, err := loadItems(root, *file)
	if err != nil {
		return err
	}
	if err := validate(items, *budget); err != nil {
		return err
	}
	listed, err := goList(root)
	if err != nil {
		return err
	}
	inFile := map[string]struct{}{}
	split := map[string][]string{}
	for _, it := range items {
		inFile[it.Path] = struct{}{}
		if it.Test != "" {
			split[it.Path] = append(split[it.Path], it.Test)
		}
	}
	if err := samePathSet(listed, inFile); err != nil {
		return err
	}
	for path, names := range split {
		want, err := listRunnableTests(root, path)
		if err != nil {
			return err
		}
		if err := sameNameSet(path, want, names); err != nil {
			return err
		}
	}
	fmt.Fprint(os.Stdout, shardStats(items))
	fmt.Printf("packages=%d split_packages=%d\n", len(inFile), len(split))
	return nil
}

func cmdPackages(args []string) error {
	fs := flag.NewFlagSet("packages", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	shard := fs.Int("shard", 0, "shard number")
	file := fs.String("file", "", "shard file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *shard < 1 || *shard > shardCount {
		return fmt.Errorf("-shard must be 1..%d", shardCount)
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	items, err := loadItems(root, *file)
	if err != nil {
		return err
	}
	seen := map[string]struct{}{}
	var paths []string
	for _, it := range items {
		if it.Shard != *shard {
			continue
		}
		if _, ok := seen[it.Path]; ok {
			continue
		}
		seen[it.Path] = struct{}{}
		paths = append(paths, it.Path)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return fmt.Errorf("shard %d has no packages", *shard)
	}
	fmt.Println(strings.Join(paths, "\n"))
	return nil
}

func cmdNeedsShell(args []string) error {
	fs := flag.NewFlagSet("needs-shell", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	shard := fs.Int("shard", 0, "shard number")
	file := fs.String("file", "", "shard file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *shard < 1 || *shard > shardCount {
		return fmt.Errorf("-shard must be 1..%d", shardCount)
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	items, err := loadItems(root, *file)
	if err != nil {
		return err
	}
	if shardNeedsShell(items, *shard) {
		fmt.Println("yes")
	} else {
		fmt.Println("no")
	}
	return nil
}

func cmdTest(args []string) error {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	shard := fs.Int("shard", 0, "shard number")
	file := fs.String("file", "", "shard file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *shard < 1 || *shard > shardCount {
		return fmt.Errorf("-shard must be 1..%d", shardCount)
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	items, err := loadItems(root, *file)
	if err != nil {
		return err
	}
	var mine []Item
	for _, it := range items {
		if it.Shard == *shard {
			mine = append(mine, it)
		}
	}
	if len(mine) == 0 {
		return fmt.Errorf("shard %d has no packages", *shard)
	}
	code := runShard(root, *shard, mine)
	if code != 0 {
		return exitCode(code)
	}
	return nil
}

func runShard(root string, shard int, items []Item) int {
	order := make([]string, 0)
	tests := map[string][]string{}
	wholeSet := map[string]bool{}
	for _, it := range items {
		if _, ok := tests[it.Path]; !ok && !wholeSet[it.Path] {
			order = append(order, it.Path)
		}
		if it.Test == "" {
			wholeSet[it.Path] = true
			continue
		}
		tests[it.Path] = append(tests[it.Path], it.Test)
	}
	var whole []string
	type spec struct{ args []string }
	var specs []spec
	for _, path := range order {
		names := tests[path]
		if len(names) == 0 {
			whole = append(whole, path)
			continue
		}
		re, err := runRegex(names)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		specs = append(specs, spec{args: []string{"test", path, "-run", re}})
		fmt.Fprintf(os.Stderr, "shard %d: %s %d tests\n", shard, path, len(names))
	}
	if len(whole) > 0 {
		sort.Strings(whole)
		specs = append(specs, spec{args: append([]string{"test"}, whole...)})
		fmt.Fprintf(os.Stderr, "shard %d: %d whole packages\n", shard, len(whole))
	}
	var cmds []*exec.Cmd
	for _, sp := range specs {
		cmd := exec.Command("go", sp.args...)
		cmd.Dir = root
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			stopCmds(cmds)
			return 2
		}
		cmds = append(cmds, cmd)
	}
	status := 0
	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			fmt.Fprintf(os.Stderr, "go %s: %v\n", strings.Join(cmd.Args[1:], " "), err)
			status = 1
		}
	}
	return status
}

func stopCmds(cmds []*exec.Cmd) {
	for _, cmd := range cmds {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	for _, cmd := range cmds {
		_ = cmd.Wait()
	}
}

func loadItems(root, file string) ([]Item, error) {
	if file == "" {
		file = filepath.Join(root, "scripts/ci/go-shards.txt")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return parseFile(string(b))
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from %s", dir)
		}
		dir = parent
	}
}

func goList(root string) ([]string, error) {
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = root
	cmd.Env = withLinux(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return nil, commandErr("go list ./...", err)
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			paths = append(paths, line)
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("go list returned no packages")
	}
	return paths, nil
}

func listRunnableTests(root, pkg string) ([]string, error) {
	cmd := exec.Command("go", "list", "-json", pkg)
	cmd.Dir = root
	cmd.Env = withLinux(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return nil, commandErr("go list -json "+pkg, err)
	}
	var listed struct {
		Dir          string
		TestGoFiles  []string
		XTestGoFiles []string
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return nil, fmt.Errorf("go list -json %s: %w", pkg, err)
	}
	var names []string
	seen := map[string]struct{}{}
	for _, name := range append(listed.TestGoFiles, listed.XTestGoFiles...) {
		body, err := os.ReadFile(filepath.Join(listed.Dir, name))
		if err != nil {
			return nil, err
		}
		found, err := testNamesIn(name, body)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for _, test := range found {
			if _, ok := seen[test]; ok {
				return nil, fmt.Errorf("%s lists %s twice", pkg, test)
			}
			seen[test] = struct{}{}
			names = append(names, test)
		}
	}
	sort.Strings(names)
	return names, nil
}

// withLinux lists the packages and tests ubuntu-latest runs. CGO_ENABLED=0
// keeps that list buildable on a darwin host; this module has no linux cgo
// files, so the set matches the CI run.
func withLinux(env []string) []string {
	out := make([]string, 0, len(env)+3)
	for _, e := range env {
		if strings.HasPrefix(e, "GOOS=") || strings.HasPrefix(e, "GOARCH=") || strings.HasPrefix(e, "CGO_ENABLED=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
}

func commandErr(name string, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		msg := strings.TrimSpace(string(exitErr.Stderr))
		if msg == "" {
			msg = exitErr.Error()
		}
		return fmt.Errorf("%s: %s", name, msg)
	}
	return fmt.Errorf("%s: %w", name, err)
}

// alignPackageTimes keeps the CI timing for every current package. A package
// that did not exist in that run is carried at 0ms. A logged package that
// no longer exists is an error, so a stale log cannot hide a deleted test.
func alignPackageTimes(list []string, logged map[string]int) (map[string]int, error) {
	inList := map[string]struct{}{}
	out := make(map[string]int, len(list))
	var missing []string
	for _, path := range list {
		inList[path] = struct{}{}
		if ms, ok := logged[path]; ok {
			out[path] = ms
			continue
		}
		missing = append(missing, path)
		out[path] = 0
	}
	var extra []string
	for path := range logged {
		if _, ok := inList[path]; !ok {
			extra = append(extra, path)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return nil, fmt.Errorf("timing log has packages go list does not: %s", strings.Join(limit(extra, 12), ", "))
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		fmt.Fprintf(os.Stderr, "warning: no CI timing, using 0ms: %s\n", strings.Join(missing, ", "))
	}
	return out, nil
}

func samePathSet(list []string, inFile map[string]struct{}) error {
	var missing, extra []string
	seen := map[string]struct{}{}
	for _, path := range list {
		seen[path] = struct{}{}
		if _, ok := inFile[path]; !ok {
			missing = append(missing, path)
		}
	}
	for path := range inFile {
		if _, ok := seen[path]; !ok {
			extra = append(extra, path)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(extra)
	var b strings.Builder
	if len(missing) > 0 {
		fmt.Fprintf(&b, "missing from shard file: %s\n", strings.Join(limit(missing, 12), ", "))
	}
	if len(extra) > 0 {
		fmt.Fprintf(&b, "not in go list: %s\n", strings.Join(limit(extra, 12), ", "))
	}
	return errors.New(strings.TrimSpace(b.String()))
}

func sameNameSet(pkg string, want, got []string) error {
	w := map[string]int{}
	g := map[string]int{}
	for _, n := range want {
		w[n]++
	}
	for _, n := range got {
		g[n]++
	}
	var missing, extra []string
	for n := range w {
		if g[n] == 0 {
			missing = append(missing, n)
		}
	}
	for n := range g {
		if w[n] == 0 {
			extra = append(extra, n)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return fmt.Errorf("%s test list drifted\nmissing: %s\nextra: %s", pkg, strings.Join(limit(missing, 12), ", "), strings.Join(limit(extra, 12), ", "))
}

func limit(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return append(append([]string{}, in[:n]...), fmt.Sprintf("…+%d", len(in)-n))
}
