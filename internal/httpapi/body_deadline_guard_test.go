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

// Risk: a new decoder, alias or form parser bypasses transport and byte bounds.
func TestHTTPRequestBodyOwnership(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if rel == "internal/httpapi/body.go" {
				return nil
			}
			fset := token.NewFileSet()
			tree, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			for _, pos := range requestBodyBypasses(tree, rel) {
				t.Errorf("%s: request body bypasses httpapi.ReadRequestBody", fset.Position(pos))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, planted := range []string{
		`io.ReadAll(r.Body)`,
		`json.NewDecoder(r.Body)`,
		`body := r.Body; io.ReadAll(body)`,
		`alias := r; io.Copy(io.Discard, alias.Body)`,
		`r.ParseForm()`,
		`r.MultipartReader()`,
	} {
		tree, err := parser.ParseFile(token.NewFileSet(), "planted.go", "package planted; import \"net/http\"; func handle(r *http.Request) { "+planted+" }", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(requestBodyBypasses(tree, "planted.go")) == 0 {
			t.Errorf("guard missed planted bypass: %s", planted)
		}
	}
}

func requestBodyBypasses(tree *ast.File, path string) []token.Pos {
	requestNames := func(fn *ast.FuncDecl) map[string]bool {
		requests := map[string]bool{}
		ast.Inspect(fn, func(n ast.Node) bool {
			field, ok := n.(*ast.Field)
			if !ok {
				return true
			}
			ptr, ok := field.Type.(*ast.StarExpr)
			if !ok {
				return true
			}
			typ, ok := ptr.X.(*ast.SelectorExpr)
			if ok && typ.Sel.Name == "Request" {
				for _, name := range field.Names {
					requests[name.Name] = true
				}
			}
			return true
		})
		// Propagate request aliases; taking the body itself is rejected below.
		for changed := true; changed; {
			changed = false
			ast.Inspect(fn, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok || len(assign.Lhs) != len(assign.Rhs) {
					return true
				}
				for i, rhs := range assign.Rhs {
					from, ok := rhs.(*ast.Ident)
					to, named := assign.Lhs[i].(*ast.Ident)
					if ok && named && requests[from.Name] && !requests[to.Name] {
						requests[to.Name] = true
						changed = true
					}
				}
				return true
			})
		}
		return requests
	}
	var bypasses []token.Pos
	for _, decl := range tree.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		requests := requestNames(fn)
		boundedStream, buffered := false, false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "BeginRequestBodyStream":
					boundedStream = true
				case "BufferRequestBodyTimeout":
					buffered = true
				}
			}
			return true
		})
		var stack []ast.Node
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return false
			}
			var parent ast.Node
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			stack = append(stack, n)
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			receiver, ok := sel.X.(*ast.Ident)
			if !ok || !requests[receiver.Name] {
				return true
			}
			switch sel.Sel.Name {
			case "Body":
				if assign, ok := parent.(*ast.AssignStmt); ok {
					for _, lhs := range assign.Lhs {
						if lhs == n {
							return true // Installing a bounded memory body is safe.
						}
					}
				}
				if compare, ok := parent.(*ast.BinaryExpr); ok && (compare.Op == token.EQL || compare.Op == token.NEQ) {
					return true // Presence checks do not consume the body.
				}
				// Attachment staging streams to disk inside BeginRequestBodyStream.
				if path == "internal/attachments/module.go" && fn.Name.Name == "upload" && boundedStream {
					if call, ok := parent.(*ast.CallExpr); ok {
						if method, ok := call.Fun.(*ast.SelectorExpr); ok && method.Sel.Name == "Stage" {
							return true
						}
					}
				}
			case "ParseForm", "ParseMultipartForm", "FormValue", "PostFormValue", "MultipartReader":
				// These parsers consume bodies buffered by the shared helper.
				if path == "internal/profile/avatar.go" && fn.Name.Name == "uploadAvatar" && buffered ||
					path == "internal/projectdraft/handoff.go" && fn.Name.Name == "formPayload" ||
					path == "internal/attachments/module.go" && fn.Name.Name == "upload" && boundedStream {
					return true
				}
			default:
				return true
			}
			bypasses = append(bypasses, n.Pos())
			return true
		})
	}
	return bypasses
}
