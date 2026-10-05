// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Complements the unchanged canonical prefix inventory in agentpairing: every
// delivery fence, mutation wrapper and explicit resource-lock site is covered.
// Source-order inventories include both branches; runtime barriers cover the
// actual competing paths. No fresh lock may follow events.Append.
func TestDeliveryResourceLockInventory(t *testing.T) {
	want := map[string]string{
		"store.go:fence":                  "project.Write",
		"store.go:mutate":                 "mutation",
		"store.go:Plan":                   "mutation plan",
		"placement.go:PlaceWithRevision":  "mutation placements",
		"store.go:mutateWithReceipt":      "fence project events",
		"store.go:projectWrite":           "project:NO KEY UPDATE",
		"store.go:lockReleases":           "release:NO KEY UPDATE",
		"store.go:Rerank":                 "mutation releases",
		"store.go:PromoteRelease":         "mutation releases",
		"store.go:SetEntryDeadline":       "mutation releases",
		"placement.go:placementLocks":     "releases items",
		"placement.go:lockItems":          "node:SHARE placement:UPDATE",
		"placement.go:place":              "placements",
		"transition.go:Transition":        "mutation rollover releases",
		"transition.go:rollover":          "releases plan items",
		"transition.go:Publish":           "mutation publicationLocks publication",
		"snapshot.go:publicationLocks":    "releases items",
		"settings.go:Update":              "mutation releases",
		"settings.go:SetDefaults":         "mutation",
		"adoption_api.go:RequestAdoption": "project.Mutation",
		"undo.go:undoPlacements":          "fence project placements",
		"undo.go:undoRank":                "fence project releases",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			sequence := resourceSequence(t, fn.Body)
			if sequence != "" {
				got[path+":"+fn.Name.Name] = sequence
			}
		}
	}
	for site, sequence := range got {
		if want[site] != sequence {
			t.Errorf("%s lock sequence %q, want %q", site, sequence, want[site])
		}
	}
	for site := range want {
		if _, ok := got[site]; !ok {
			t.Errorf("missing resource lock site %s", site)
		}
	}
}

func resourceSequence(t *testing.T, body *ast.BlockStmt) string {
	t.Helper()
	sequence := []string{}
	ast.Inspect(body, func(n ast.Node) bool {
		label := ""
		switch n := n.(type) {
		case *ast.CallExpr:
			name := ""
			switch f := n.Fun.(type) {
			case *ast.Ident:
				name = f.Name
			case *ast.SelectorExpr:
				if receiver, ok := f.X.(*ast.Ident); ok {
					name = receiver.Name + "." + f.Sel.Name
				}
			}
			label = map[string]string{"authz.LockProjectMutation": "project.Mutation", "w.publicationLocks": "publicationLocks", "fence": "fence", "authz.LockProjectWrite": "project.Write", "s.mutate": "mutation", "s.mutateWithReceipt": "mutation", "s.projectWrite": "project", "w.lockReleases": "releases", "w.lockItems": "items", "w.placementLocks": "placements", "w.place": "placements", "w.rollover": "rollover", "w.plan": "plan", "events.Append": "events", "proof.Settle": "publication"}[name]
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				break
			}
			sql, err := strconv.Unquote(n.Value)
			if err != nil {
				t.Fatal(err)
			}
			sql = strings.Join(strings.Fields(sql), " ")
			if strings.Contains(sql, "pg_advisory") {
				label = "advisory"
				break
			}
			mode := ""
			for _, candidate := range []string{"NO KEY UPDATE", "SHARE", "UPDATE"} {
				if strings.Contains(sql, " FOR "+candidate) {
					mode = candidate
					break
				}
			}
			if mode == "" {
				break
			}
			for _, table := range []struct{ sql, label string }{{"FROM project_delivery", "project"}, {"FROM project_releases", "release"}, {"FROM nodes", "node"}, {"FROM ships_in", "placement"}, {"FROM tenants", "tenant"}} {
				if strings.Contains(sql, table.sql) {
					label = table.label + ":" + mode
					break
				}
			}
			if label == "" {
				label = "unknown:" + mode
			}
		}
		if label != "" {
			sequence = append(sequence, label)
		}
		return true
	})
	return strings.Join(sequence, " ")
}

func TestDeliveryResourceLockNegativeControls(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"w.lockItems(ids); w.lockReleases(ids)", "items releases"},
		{"events.Append(ctx,tx,p,change); w.lockItems(ids)", "events items"},
		{"tx.QueryRow(ctx,`SELECT id FROM tenants FOR SHARE`); fence(ctx,tx,p,project,permission)", "tenant:SHARE fence"},
		{"events.Append(ctx,tx,p,change); authz.LockProjectWrite(ctx,tx,p.TenantID)", "events project.Write"},
		{"w.lockItems(ids); authz.LockProjectWrite(ctx,tx,p.TenantID)", "items project.Write"},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package delivery; func fixture(){"+tc.body+"}", 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := resourceSequence(t, file.Decls[0].(*ast.FuncDecl).Body); got != tc.want {
			t.Fatalf("negative control parsed %q, want %q", got, tc.want)
		}
	}
}
