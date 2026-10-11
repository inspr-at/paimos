// SPDX-License-Identifier: AGPL-3.0-only
package httpapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// Risk: a new HTTP body deadline bypasses size bounds and connection disposal.
// The one raw Unix hook socket deadline is a separate transport, not HTTP.
func TestHTTPReadDeadlineOwnership(t *testing.T) {
	root := filepath.Join("..", "..")
	count := 0
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			tree, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(tree, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "SetReadDeadline" {
					return true
				}
				rel, _ := filepath.Rel(root, path)
				rel = filepath.ToSlash(rel)
				if rel == "internal/httpapi/body.go" {
					count++
					return true
				}
				// hooknote owns a raw bounded Unix socket reader, not a ResponseController.
				if rel == "internal/hooknote/server.go" {
					if receiver, ok := sel.X.(*ast.SelectorExpr); ok && receiver.Sel.Name == "conn" {
						return true
					}
				}
				t.Errorf("%s sets a read deadline outside httpapi.ReadRequestBody", rel)
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if count == 0 {
		t.Fatal("guard never found the body-read helper's deadline calls")
	}
}
