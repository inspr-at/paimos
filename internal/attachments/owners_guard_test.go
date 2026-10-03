// SPDX-License-Identifier: AGPL-3.0-only
package attachments

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Catch new Store consumers (including aliases and method values) before they
// can publish files absent from the shared inventory. Source rather than runtime
// registration makes even an unexercised new upload path fail this guard.
func TestBlobWritersHaveRegisteredOwners(t *testing.T) {
	root := filepath.Join("..", "..")
	registered := map[string]Owner{}
	seen := map[string]bool{}
	for _, spec := range blobOwners() {
		if spec.query == "" || len(spec.writers) == 0 {
			t.Fatalf("incomplete owner %q", spec.owner)
		}
		for _, writer := range spec.writers {
			if _, exists := registered[writer]; exists {
				t.Fatalf("duplicate writer %s", writer)
			}
			registered[writer] = spec.owner
		}
	}
	// These are the shared protocol implementations, not independent owners.
	protocol := map[string]bool{"internal/attachments/storage.go:Put": true, "internal/attachments/storage.go:PutProfileAsset": true}
	files := map[string]*ast.File{}
	storePackages := map[string]bool{"internal/attachments": true}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		files[rel] = file
		for _, imp := range file.Imports {
			value, _ := strconv.Unquote(imp.Path.Value)
			if value == "github.com/inspr-at/paimos/internal/attachments" {
				storePackages[filepath.Dir(rel)] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel, file := range files {
		usesStore := storePackages[filepath.Dir(rel)]
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			writer := rel + ":" + fn.Name.Name
			direct := map[ast.Expr]bool{}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok {
					direct[call.Fun] = true
				}
				return true
			})
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if sel, ok := node.(*ast.SelectorExpr); ok && usesStore && !direct[sel] && (sel.Sel.Name == "Put" || sel.Sel.Name == "Publish" || sel.Sel.Name == "PutProfileAsset") {
					t.Errorf("indirect blob writer in %s: use a direct call with an inventoried owner", writer)
				}
				return true
			})
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch f := call.Fun.(type) {
				case *ast.SelectorExpr:
					name = f.Sel.Name
				case *ast.Ident:
					name = f.Name
				}
				if usesStore && (name == "Put" || name == "Publish" || name == "PutProfileAsset") {
					if protocol[writer] {
						return true
					}
					owner, ok := registered[writer]
					if !ok {
						t.Errorf("unregistered blob writer %s: add its durable references to blobOwners", writer)
						return true
					}
					seen[writer] = true
					if name == "PutProfileAsset" {
						if owner != OwnerQuoteProfile {
							t.Errorf("%s has wrong profile asset owner", writer)
						}
					} else {
						index := 2
						if name == "Put" {
							index = 3
						}
						if len(call.Args) <= index {
							t.Errorf("%s has no owner", writer)
							return true
						}
						arg := call.Args[index]
						ownerName := ""
						switch x := arg.(type) {
						case *ast.Ident:
							ownerName = x.Name
						case *ast.SelectorExpr:
							ownerName = x.Sel.Name
						}
						want := map[Owner]string{OwnerAttachment: "OwnerAttachment", OwnerAvatar: "OwnerAvatar", OwnerReceipt: "OwnerReceipt", OwnerQuoteProfile: "OwnerQuoteProfile"}[owner]
						if ownerName != want {
							t.Errorf("%s publishes %s, inventory expects %s", writer, ownerName, want)
						}
					}
				}
				return true
			})
			// Any direct hash-layout writer must be moved behind the protocol.
			// This also catches the former quote font/SVG filepath.Join bypass.
			if rel != "internal/attachments/storage.go" {
				ast.Inspect(fn.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					f, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || f.Sel.Name != "Join" {
						return true
					}
					for _, arg := range call.Args {
						slice, ok := arg.(*ast.SliceExpr)
						if !ok {
							continue
						}
						hi, ok := slice.High.(*ast.BasicLit)
						if ok && hi.Value == "2" && slice.Low == nil {
							t.Errorf("direct hash layout outside Store: %s", writer)
						}
					}
					return true
				})
			}
		}
	}
	for writer := range registered {
		if !seen[writer] {
			t.Errorf("stale owner writer registration %s", writer)
		}
	}
}
