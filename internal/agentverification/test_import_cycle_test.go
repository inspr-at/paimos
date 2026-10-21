// SPDX-License-Identifier: AGPL-3.0-only

package agentverification

import (
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Risk: an in-package test imports a native-control dependency that imports
// the package under test back. go test then cannot compile that package.
// External tests are a separate package and do not close this cycle.
// This failed on c03d0fc8: agentpairing/capabilities_test.go imported agentd,
// nativequalification/qualification_test.go imported agentd, and
// questions/questions_test.go imported nodes.
func TestReleaseQualificationPackagesHaveNoTestImportCycle(t *testing.T) {
	root := moduleRoot(t)
	modulePath := modulePathOf(t, root)
	targets := []string{
		modulePath + "/internal/agentpairing",
		modulePath + "/internal/nativequalification",
		modulePath + "/internal/questions",
	}
	for _, platform := range []struct{ goos, goarch string }{
		{"linux", "amd64"},
		{"darwin", "arm64"},
	} {
		prod, extra, edges := indexImports(t, root, modulePath, platform.goos, platform.goarch)
		for _, target := range targets {
			if cycle := testImportCycle(target, prod, extra); cycle != nil {
				t.Errorf("%s/%s test import cycle:\n%s", platform.goos, platform.goarch, formatCycle(cycle, edges))
			}
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test file path unavailable")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test file")
		}
		dir = parent
	}
}

func modulePathOf(t *testing.T, root string) string {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	line, _, _ := strings.Cut(string(text), "\n")
	path, ok := strings.CutPrefix(line, "module ")
	if !ok || path == "" || strings.ContainsAny(path, " \t") {
		t.Fatal("go.mod has no module path")
	}
	return path
}

type importEdges struct {
	prod, extra map[[2]string]string
}

// indexImports reads production imports and in-package test imports. An
// external test package (name ending in _test) is omitted: it is not part of
// the package go test augments.
func indexImports(t *testing.T, root, modulePath, goos, goarch string) (prod, extra map[string][]string, edges importEdges) {
	t.Helper()
	ctx := build.Default
	ctx.GOOS = goos
	ctx.GOARCH = goarch
	prod = map[string][]string{}
	extra = map[string][]string{}
	edges.prod = map[[2]string]string{}
	edges.extra = map[[2]string]string{}
	sawPairingTest := false
	for _, tree := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := entry.Name()
			if entry.IsDir() {
				if name == "testdata" || name == "vendor" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(name, ".go") {
				return nil
			}
			match, err := ctx.MatchFile(filepath.Dir(path), name)
			if err != nil {
				return err
			}
			if !match {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if filepath.ToSlash(rel) == "internal/agentpairing/capabilities_test.go" {
				sawPairingTest = true
			}
			pkg := modulePath + "/" + filepath.ToSlash(filepath.Dir(rel))
			external := strings.HasSuffix(name, "_test.go") && strings.HasSuffix(file.Name.Name, "_test")
			internalTest := strings.HasSuffix(name, "_test.go") && !external
			for _, spec := range file.Imports {
				imported, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				if !strings.HasPrefix(imported, modulePath+"/") || external {
					continue
				}
				key := [2]string{pkg, imported}
				relPath := filepath.ToSlash(rel)
				if internalTest {
					if _, ok := edges.extra[key]; !ok {
						edges.extra[key] = relPath
					}
					extra[pkg] = append(extra[pkg], imported)
					continue
				}
				if _, ok := edges.prod[key]; !ok {
					edges.prod[key] = relPath
				}
				prod[pkg] = append(prod[pkg], imported)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !sawPairingTest {
		t.Fatal("capabilities_test.go was not indexed")
	}
	for _, set := range []map[string][]string{prod, extra} {
		for pkg, imports := range set {
			slices.Sort(imports)
			set[pkg] = slices.Compact(imports)
		}
	}
	return prod, extra, edges
}

func testImportCycle(start string, prod, extra map[string][]string) []string {
	seen := map[string]bool{}
	queue := [][]string{{start}}
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		current := path[len(path)-1]
		outs := append([]string{}, prod[current]...)
		if len(path) == 1 {
			outs = append(outs, extra[current]...)
		}
		slices.Sort(outs)
		outs = slices.Compact(outs)
		for _, next := range outs {
			if next == start {
				return append(append([]string{}, path...), next)
			}
			if seen[next] {
				continue
			}
			seen[next] = true
			queue = append(queue, append(append([]string{}, path...), next))
		}
	}
	return nil
}

func formatCycle(cycle []string, edges importEdges) string {
	var b strings.Builder
	for i := 0; i < len(cycle)-1; i++ {
		from, to := cycle[i], cycle[i+1]
		key := [2]string{from, to}
		file := ""
		if i == 0 {
			file = edges.extra[key]
		}
		if file == "" {
			file = edges.prod[key]
		}
		if file == "" {
			file = from
		}
		b.WriteString(file)
		b.WriteString(" imports ")
		b.WriteString(to)
		b.WriteByte('\n')
	}
	return b.String()
}
