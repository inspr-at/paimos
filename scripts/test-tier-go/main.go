// SPDX-License-Identifier: AGPL-3.0-only
// Browser-free, cross-platform source inventory, with the current target's
// active files/import graph supplied by go list. Native -list is checked by
// the tier runner before executing selected packages.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type test struct {
	Package string `json:"package"`
	Name    string `json:"name"`
	File    string `json:"file"`
	Active  bool   `json:"active"`
}
type pkg struct {
	Dir                                                           string
	ImportPath                                                    string
	Imports, TestImports, XTestImports, TestGoFiles, XTestGoFiles []string
}

func sourceTests(file string, body any, active bool) ([]test, error) {
	f, err := parser.ParseFile(token.NewFileSet(), file, body, 0)
	if err != nil {
		return nil, err
	}
	aliases := map[string]bool{}
	for _, imp := range f.Imports {
		if imp.Path.Value != `"testing"` {
			continue
		}
		name := "testing"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		aliases[name] = true
	}
	var tests []test
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
			continue
		}
		name := fn.Name.Name
		if name == "TestMain" || !(strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Fuzz")) {
			continue
		}
		suffix := name[4:]
		if suffix != "" {
			r, _ := utf8.DecodeRuneInString(suffix)
			if unicode.IsLower(r) {
				continue
			}
		}
		star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		typ, ok := star.X.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		qualifier, ok := typ.X.(*ast.Ident)
		if !ok || !aliases[qualifier.Name] {
			continue
		}
		if strings.HasPrefix(name, "Test") && typ.Sel.Name != "T" || strings.HasPrefix(name, "Fuzz") && typ.Sel.Name != "F" {
			continue
		}
		tests = append(tests, test{filepath.ToSlash(filepath.Dir(file)), name, file, active})
	}
	return tests, nil
}

func inventory() (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "list", "-json", "./...")
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list failed: %w", err)
	}
	active := map[string]bool{}
	graph := map[string][]string{}
	module, err := os.ReadFile("go.mod")
	if err != nil {
		return nil, err
	}
	prefix := strings.Fields(string(module))[1] + "/"
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var p pkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		path := strings.TrimPrefix(p.ImportPath, prefix)
		graph[path] = []string{}
		for _, file := range append(p.TestGoFiles, p.XTestGoFiles...) {
			active[filepath.ToSlash(filepath.Join(path, file))] = true
		}
		for _, imported := range append(append(p.Imports, p.TestImports...), p.XTestImports...) {
			if strings.HasPrefix(imported, prefix) {
				graph[path] = append(graph[path], strings.TrimPrefix(imported, prefix))
			}
		}
	}
	files, err := exec.CommandContext(ctx, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*_test.go").Output()
	if err != nil {
		return nil, err
	}
	var tests []test
	for _, file := range strings.Split(string(files), "\x00") {
		if file == "" || strings.Contains(file, "/testdata/") {
			continue
		}
		found, err := sourceTests(file, nil, active[file])
		if err != nil {
			return nil, err
		}
		tests = append(tests, found...)
	}
	sort.Slice(tests, func(i, j int) bool { return tests[i].File+tests[i].Name < tests[j].File+tests[j].Name })
	return map[string]any{"tests": tests, "imports": graph}, nil
}

func main() {
	data, err := inventory()
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(data)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
