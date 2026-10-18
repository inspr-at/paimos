// SPDX-License-Identifier: AGPL-3.0-only
// Parse one owner's test sources without compiling any test binary. The
// uncached repo collector remains the golden authority for this inventory.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

type source struct {
	File   string `json:"file"`
	Active bool   `json:"active"`
}
type request struct {
	Package string   `json:"package"`
	Files   []source `json:"files"`
}
type test struct {
	Package string `json:"package"`
	Name    string `json:"name"`
	File    string `json:"file"`
	Active  bool   `json:"active"`
}

func collect(input request) ([]test, error) {
	if !filepath.IsLocal(input.Package) || len(input.Files) > 4096 {
		return nil, fmt.Errorf("invalid package source request")
	}
	tests := []test{}
	var totalBytes int64
	for _, row := range input.Files {
		if !filepath.IsLocal(row.File) || filepath.ToSlash(filepath.Dir(row.File)) != input.Package || !strings.HasSuffix(row.File, "_test.go") {
			return nil, fmt.Errorf("source outside requested package")
		}
		stat, err := os.Lstat(row.File)
		if err != nil {
			return nil, err
		}
		totalBytes += stat.Size()
		if !stat.Mode().IsRegular() || stat.Size() > 32*1024*1024 || totalBytes > 64*1024*1024 {
			return nil, fmt.Errorf("invalid or oversized test source")
		}
		f, err := parser.ParseFile(token.NewFileSet(), row.File, nil, 0)
		if err != nil {
			return nil, err
		}
		aliases := map[string]bool{}
		for _, imp := range f.Imports {
			if imp.Path.Value == `"testing"` {
				name := "testing"
				if imp.Name != nil {
					name = imp.Name.Name
				}
				aliases[name] = true
			}
		}
		for _, declaration := range f.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
				continue
			}
			name := fn.Name.Name
			if name == "TestMain" || !(strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Fuzz")) {
				continue
			}
			if suffix := name[4:]; suffix != "" {
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
			tests = append(tests, test{input.Package, name, row.File, row.Active})
		}
	}
	return tests, nil
}

func main() {
	var input request
	err := json.NewDecoder(io.LimitReader(os.Stdin, 4*1024*1024)).Decode(&input)
	if err == nil {
		var tests []test
		tests, err = collect(input)
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(tests)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
