// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This explicit caller inventory makes newly connected entry paths reviewable.
// Actual contention and FK compatibility are exercised by boundary/recurrence tests.
func TestSharedFenceCallerInventory(t *testing.T) {
	expected := map[string][]string{
		"db.LockTree":               {"authz/project_members.go", "db/fences.go", "modelregistry/module.go", "modelregistry/preparation.go", "operatoractor/actor.go", "workorders/common.go"},
		"db.LockTenant":             {"auth/store.go", "db/fences.go", "modelregistry/module.go", "modelregistry/preferences_http.go", "modelregistry/preparation.go", "modelregistry/routes_write.go", "workorders/common.go"},
		"db.LockCurrentTree":        {"agentpairing/lifecycle.go", "nodes/module.go"},
		"agentpairing.LockRead":     {"agentaccounts/residency_evidence.go", "agentruns/runs.go"},
		"agentpairing.Lock":         {"agentaccounts/route.go", "agentpairing/lifecycle.go", "agentruns/runs.go", "agentruns/telemetry.go", "crossreview/module.go", "knowledge/tagger.go", "knowledge/undo.go", "modelregistry/preparation.go", "nodes/bulk.go", "nodes/nodes.go"},
		"agentpairing.LockMutation": {"agentaccounts/module.go", "agentpairing/module.go", "agentruns/queue.go", "auth/owner_workstation.go", "portal/market.go", "portal/moderate.go", "portal/module.go", "portal/products.go"},
		"authz.LockProjectMutation": {"authz/agent_creation.go", "authz/members.go", "authz/project_members.go", "importer/users_backfill.go", "importer/writer.go"},
		"authz.LockProjectWrite":    {"attachments/module.go", "decisiondesk/notifications.go", "nodes/portal_publish.go"},
		"operatoractor.Ensure":      {"auth/store.go", "authz/operator.go", "journey/operator.go", "operatoractor/actor.go"},
		"rules.PrepareWrite":        {"knowledge/learning_draft.go"},
	}
	root := ".."
	observed := map[string]map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			key := ""
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				if pkg, ok := fn.X.(*ast.Ident); ok {
					key = pkg.Name + "." + fn.Sel.Name
				}
			case *ast.Ident:
				key = file.Name.Name + "." + fn.Name
			}
			if key == "operatoractor.EnsureWithProduction" {
				key = "operatoractor.Ensure"
			}
			if _, ok := expected[key]; ok {
				if observed[key] == nil {
					observed[key] = map[string]bool{}
				}
				observed[key][filepath.ToSlash(rel)] = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range expected {
		got := []string{}
		for path := range observed[key] {
			got = append(got, path)
		}
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s callers changed; inventory and analyze entry order: got %v want %v", key, got, want)
		}
	}
}

func TestSharedFencePrimitiveOrder(t *testing.T) {
	checks := []struct{ path, fn, first, second string }{
		{"../db/fences.go", "LockTree", "LockTenant(", "pg_advisory_xact_lock"},
		{"lifecycle.go", "Lock", "db.LockCurrentTree(", "aeon-pairing:"},
		{"../auth/store.go", "revokeAgentKeyTx", "db.LockTenant(", "lockAgentKey("},
		{"../rules/module.go", "endpoint", "lockAccess(", "pg_advisory_xact_lock"},
		{"../rules/learning_draft.go", "PrepareWrite", "lockAccess(", "pg_advisory_xact_lock"},
		{"../recurrences/module.go", "lock", "FOR NO KEY UPDATE", "pg_try_advisory_xact_lock"},
	}
	for _, c := range checks {
		raw, err := os.ReadFile(c.path)
		if err != nil {
			t.Fatal(err)
		}
		tree, err := parser.ParseFile(token.NewFileSet(), c.path, raw, 0)
		if err != nil {
			t.Fatal(err)
		}
		var body string
		for _, decl := range tree.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name.Name == c.fn {
				body = string(raw[int(fn.Pos())-1 : int(fn.End())-1])
				break
			}
		}
		first, second := strings.Index(body, c.first), strings.Index(body, c.second)
		if first < 0 || second < 0 || first >= second {
			t.Errorf("%s.%s must acquire %s before %s", c.path, c.fn, c.first, c.second)
		}
	}
}

// TestTenantTreePairingLockOrder inventories every direct advisory/tenant lock
// and lock-helper call in these seven packages. The order is tenant -> tree ->
// pairing -> resource rows; access-only writers omit pairing/tree. Keep
// the exact primitive sequences here: checking only callers missed the previous
// inversions inside shared helpers. New call sites must join this inventory.
// Alternative branches (recurrence try/blocking, operator/HTTP) appear in source
// order; repeated helpers are reentrant, not new acquisitions after row locks.
func TestTenantTreePairingLockOrder(t *testing.T) {
	want := map[string]string{
		"agentaccounts/module.go:in":                        "pairing.Mutation",
		"agentaccounts/route.go:reserve":                    "pairing.Lock",
		"agentaccounts/route.go:ValidateReservedCapacity":   "pairing.Lock",
		"agentpairing/provision.go:approve":                 "tenant:NO KEY UPDATE pairing.Lock",
		"agentpairing/module.go:in":                         "pairing.Mutation",
		"agentpairing/lifecycle.go:Lock":                    "tree.Mutation pairing",
		"agentpairing/lifecycle.go:LockRead":                "pairing",
		"agentpairing/lifecycle.go:LockMutation":            "pairing.Lock",
		"authz/accept.go:AcceptInvite":                      "alias.Lock",
		"authz/agent_creation.go:createAgent":               "project.Mutation",
		"authz/aliases.go:linkAlias":                        "access.Mutation",
		"authz/aliases.go:unlinkAlias":                      "access.Mutation",
		"authz/invites.go:createInvite":                     "access.Mutation",
		"authz/invites.go:retryInviteProvision":             "access.Mutation",
		"authz/invites.go:revokeInvite":                     "access.Mutation",
		"authz/lifecycle.go:setStatus":                      "access.Mutation",
		"authz/members.go:setWorkspaceRoleTx":               "project.Mutation access.Mutation",
		"authz/module.go:authorizeMutation":                 "tenant:UPDATE",
		"authz/module.go:createRole":                        "access.Mutation",
		"authz/module.go:patchRole":                         "access.Mutation",
		"authz/module.go:deleteRole":                        "access.Mutation",
		"authz/operator.go:OperatorBindWorkspaceRole":       "operator.Ensure",
		"authz/operator.go:OperatorUnbindWorkspaceRole":     "operator.Ensure",
		"authz/operator.go:OperatorBindProjects":            "operator.Ensure",
		"authz/operator.go:OperatorUnbindProject":           "operator.Ensure",
		"authz/project_members.go:authorizeProjectMutation": "project.Mutation",
		"authz/project_members.go:LockProjectMutation":      "tree.Mutation",
		"authz/project_members.go:LockProjectWrite":         "tree.Mutation",
		"authz/project_members.go:setProjectBindingTx":      "project.Mutation project.Authorize",
		"authz/project_members.go:removeProjectBindingTx":   "project.Mutation project.Authorize",
		"recurrences/module.go:lock":                        "tenant:NO KEY UPDATE tree:try tree",
		"recurrences/module.go:create":                      "recurrence.lock",
		"recurrences/module.go:update":                      "recurrence.lock",
		"recurrences/module.go:setPaused":                   "recurrence.lock",
		"recurrences/engine.go:RunTenant":                   "recurrence.lock",
		"recurrences/engine.go:recordFailure":               "recurrence.lock",
		"recurrences/engine.go:syncPublications":            "recurrence.lock",
		"recurrences/occurrence.go:ensureActor":             "advisory:recurring-actor",
		"recurrences/occurrence.go:runNow":                  "recurrence.lock",
		"operatoractor/actor.go:Ensure":                     "operator.EnsureWithProduction",
		"operatoractor/actor.go:EnsureWithProduction":       "tree.Mutation",

		"nodes/bulk.go:applyBulk":                     "pairing.Lock",
		"nodes/module.go:lockTree":                    "tree.Mutation",
		"nodes/nodes.go:updateNode":                   "pairing.Lock",
		"nodes/nodes.go:deleteNode":                   "pairing.Lock",
		"nodes/portal_publish.go:armPortalModeration": "project.Write",
		"portal/market.go:manage":                     "pairing.Mutation",
		"portal/moderate.go:moderate":                 "pairing.Mutation",
		"portal/module.go:updateSettings":             "pairing.Mutation",
		"portal/products.go:publicWriteProduct":       "pairing.Mutation",
		// The existing scanner labels direct seed-0 advisories "tree";
		// this key is portal-public-service:<tenant>, after the mutation fence.
		"portal/public.go:serviceActor": "tree",
	}
	got := map[string]string{}
	for _, pkg := range []string{"agentaccounts", "agentpairing", "authz", "recurrences", "operatoractor", "nodes", "portal"} {
		files, err := filepath.Glob(filepath.Join("..", pkg, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("inventory %s: files=%d err=%v", pkg, len(files), err)
		}
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
				sequence := lockOrderSequence(t, pkg, fn.Body)
				if len(sequence) > 0 {
					got[pkg+"/"+filepath.Base(path)+":"+fn.Name.Name] = strings.Join(sequence, " ")
				}
			}
		}
	}
	for site, sequence := range got {
		if expected, ok := want[site]; !ok || sequence != expected {
			t.Errorf("%s: lock sequence %q, want %q (tenant -> tree -> pairing)", site, sequence, expected)
		}
	}
	for site := range want {
		if _, ok := got[site]; !ok {
			t.Errorf("missing lock site %s", site)
		}
	}
	if reflect.DeepEqual(got, want) {
		t.Logf("verified %d lock sites across seven packages", len(got))
	}
}

// TestOperatorLockOrderNegativeControls proves that the inventory distinguishes
// the same calls in opposite orders, including locks hidden in operator helpers.
// Exact rejected sequences ensure that missing helper recognition cannot make
// a negative control pass just because its sequence differs from the allowlist.
func TestOperatorLockOrderNegativeControls(t *testing.T) {
	for _, helper := range []string{"Ensure", "EnsureWithProduction"} {
		t.Run(helper, func(t *testing.T) {
			args := "ctx, tx, tenantID"
			if helper == "EnsureWithProduction" {
				args += ", true"
			}
			call := "operatoractor." + helper + "(" + args + ")"
			label := "operator." + helper
			for _, tc := range []struct {
				name, ordered, inverted, want, wantInverted string
			}{
				{
					name:         "pairing_before_operator",
					ordered:      "agentpairing.Lock(ctx, tx, tenantID); " + call,
					inverted:     call + "; agentpairing.Lock(ctx, tx, tenantID)",
					want:         "pairing.Lock " + label,
					wantInverted: label + " pairing.Lock",
				},
				{
					name:         "operator_before_tenant_reentry",
					ordered:      call + "; tx.QueryRow(ctx, `SELECT id FROM tenants WHERE id=$1 FOR UPDATE`)",
					inverted:     "tx.QueryRow(ctx, `SELECT id FROM tenants WHERE id=$1 FOR UPDATE`); " + call,
					want:         label + " tenant:UPDATE",
					wantInverted: "tenant:UPDATE " + label,
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					sequence := func(body string) string {
						file, err := parser.ParseFile(token.NewFileSet(), "operator.go", "package authz; func fixture() { "+body+" }", 0)
						if err != nil {
							t.Fatal(err)
						}
						fn := file.Decls[0].(*ast.FuncDecl)
						return strings.Join(lockOrderSequence(t, "authz", fn.Body), " ")
					}
					if got := sequence(tc.ordered); got != tc.want {
						t.Fatalf("ordered lock sequence %q, want %q", got, tc.want)
					}
					got := sequence(tc.inverted)
					if got != tc.wantInverted {
						t.Fatalf("inverted lock sequence %q, want %q", got, tc.wantInverted)
					}
					if got == tc.want {
						t.Fatalf("inventory accepted inverted helper sequence %q", got)
					}
				})
			}
		})
	}
}

// lockOrderSequence is shared by the repository inventory and its controls.
func lockOrderSequence(t *testing.T, pkg string, body *ast.BlockStmt) []string {
	t.Helper()
	var sequence []string
	ast.Inspect(body, func(n ast.Node) bool {
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
			label := map[string]string{
				"db.LockTree": "tree.Mutation", "db.LockCurrentTree": "tree.Mutation", "db.LockTenant": "tenant.NO_KEY_UPDATE",
				"apply.Lock":        "alias.Lock",
				"agentpairing.Lock": "pairing.Lock", "agentpairing.LockMutation": "pairing.Mutation",
				"LockProjectMutation": "project.Mutation",
				"LockProjectWrite":    "project.Write", "authz.LockProjectMutation": "project.Mutation",
				"authz.LockProjectWrite": "project.Write", "m.authorizeMutation": "access.Mutation",
				"m.authorizeProjectMutation":         "project.Authorize",
				"operatoractor.Ensure":               "operator.Ensure",
				"operatoractor.EnsureWithProduction": "operator.EnsureWithProduction",
			}[name]
			if pkg == "agentpairing" {
				if name == "Lock" {
					label = "pairing.Lock"
				}
				if name == "LockMutation" {
					label = "pairing.Mutation"
				}
			}
			if pkg == "recurrences" && name == "lock" {
				label = "recurrence.lock"
			}
			if pkg == "operatoractor" && name == "EnsureWithProduction" {
				label = "operator.EnsureWithProduction"
			}
			if label != "" {
				sequence = append(sequence, label)
			}
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				break
			}
			sql, err := strconv.Unquote(n.Value)
			if err != nil {
				t.Fatal(err)
			}
			sql = strings.Join(strings.Fields(sql), " ")
			label := ""
			if strings.Contains(sql, "pg_advisory") || strings.Contains(sql, "pg_try_advisory") {
				switch {
				case strings.Contains(sql, "aeon-pairing:"):
					label = "pairing"
				case strings.Contains(sql, "aeon-recurring-actor:"):
					label = "advisory:recurring-actor"
				case strings.Contains(sql, ",532)"):
					label = "advisory:alias"
				default:
					label = "tree"
				}
				if strings.Contains(sql, "pg_try_advisory") {
					label += ":try"
				}
			} else if strings.Contains(sql, "FROM tenants ") && strings.Contains(sql, " FOR ") {
				_, mode, _ := strings.Cut(sql, " FOR ")
				label = "tenant:" + mode
			}
			if label != "" {
				sequence = append(sequence, label)
			}
		}
		return true
	})
	return sequence
}
