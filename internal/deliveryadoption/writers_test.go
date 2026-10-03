// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// An explicit inventory forces a new UPDATE nodes writer to declare whether it
// can reach an adopted release. Runtime guards are exercised separately; typed
// writers cannot assume that an imported release will remain a generic node.
var nodeWriters = map[string]string{
	"agentruns/queue.go":                "loadTicket permits work kinds only",
	"business/crm/adjacent.go":          "typed contact/company/project-group loaders",
	"business/crm/providers.go":         "typed external-system loader",
	"business/crm/records.go":           "typed company/contact loaders",
	"business/crm/undo.go":              "typed CRM record loader",
	"business/quotes/delete.go":         "typed quote loader",
	"business/quotes/drafts.go":         "typed quote loader",
	"business/quotes/showcase_write.go": "typed quote showcase loader",
	"business/quotes/undo.go":           "typed quote loader",
	"deliveryadoption/apply.go":         "fenced atomic adoption; default release title only",
	"importer/offers/import.go":         "typed quote import",
	"importer/users_backfill.go":        "RefuseReleaseNodes before assignments",
	"importer/writer.go":                "RefuseReleaseNodes before imported mutation",
	"intake/draft.go":                   "typed intake-draft loader",
	"knowledge/learnings.go":            "typed knowledge loader",
	"knowledge/module.go":               "typed knowledge loader",
	"knowledge/tagger.go":               "typed knowledge loader",
	"knowledge/undo.go":                 "typed knowledge loader",
	"nodes/bulk.go":                     "refuseReleaseNode and batch undo guard",
	"nodes/convert.go":                  "refuseReleaseNode and kind undo guard",
	"nodes/nodes.go":                    "guardReleasePatch; delete/move guard; restore is harmless",
	"nodes/project_move.go":             "refuseReleaseNode plus subtree/placement guards",
	"nodes/tags.go":                     "loadTag and RefuseReleaseNodes before assignment rewrite",
	"nodes/undo.go":                     "refuseReleaseNode before restoreMovedNode",
	"portal/moderate.go":                "typed public wish/product/feature loader",
	"requirements/disposable.go":        "typed disposable requirement loader",
	"rules/store.go":                    "typed rule loader",
	"statusautopilot/engine.go":         "loadCandidates excludes project_releases for apply and undo",
	"statusautopilot/pickup.go":         "typed ticket/task loader",
	"workqueue/enqueue.go":              "typed work-item loader",
	"workqueue/removal.go":              "typed work-item loader",
}
var updatesNodes = regexp.MustCompile(`(?is)\bUPDATE\s+nodes(?:\s+(?:AS\s+)?[a-z_]+)?\s+SET\s+`)
var writesState = regexp.MustCompile(`(?is)(?:\bSET|,)\s*state\s*=`)
var writesUpdated = regexp.MustCompile(`(?is)(?:\bSET|,)\s*updated_at\s*=`)

func checkNodeWriter(path, source string) (int, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
	if err != nil {
		return 0, err
	}
	n := 0
	ast.Inspect(file, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		sql, e := strconv.Unquote(lit.Value)
		if e != nil || !updatesNodes.MatchString(sql) {
			return true
		}
		n++
		if nodeWriters[path] == "" {
			err = fmt.Errorf("unclassified node writer %s", path)
		}
		if writesState.MatchString(sql) && !writesUpdated.MatchString(sql) {
			err = fmt.Errorf("state mutation without updated_at bump in %s", path)
		}
		return true
	})
	return n, err
}
func TestNodeWriterInventoryAndStateTimestampBumps(t *testing.T) {
	root := ".."
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		n, err := checkNodeWriter(filepath.ToSlash(relative), string(raw))
		if n > 0 {
			seen[filepath.ToSlash(relative)] = true
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for path := range nodeWriters {
		if !seen[path] {
			t.Errorf("stale writer classification %s", path)
		}
	}
}
func TestNodeWriterScanRejectsUnclassifiedAndUnstampedMutations(t *testing.T) {
	for _, tc := range []struct{ path, sql string }{{"new/writer.go", "UPDATE nodes SET state='done',updated_at=now()"}, {"nodes/nodes.go", "UPDATE nodes SET state='done'"}} {
		source := "package sample\nconst sql=" + strconv.Quote(tc.sql)
		if _, err := checkNodeWriter(tc.path, source); err == nil {
			t.Fatalf("writer scan accepted %s", tc.sql)
		}
	}
}
