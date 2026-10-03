// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestTenantTreePairingLockOrder inventories every direct advisory/tenant lock
// and lock-helper call in these five packages. The shipped order is pairing ->
// tree -> tenant -> resource rows; access-only writers omit pairing/tree. Keep
// the exact primitive sequences here: checking only callers missed the previous
// inversions inside shared helpers. New call sites must join this inventory.
// Alternative branches (recurrence try/blocking, operator/HTTP) appear in source
// order; repeated helpers are reentrant, not new acquisitions after row locks.
func TestTenantTreePairingLockOrder(t *testing.T) {
	want := map[string]string{
		"agentaccounts/module.go:in":                        "pairing.Lock",
		"agentaccounts/route.go:reserve":                    "pairing.Lock",
		"agentaccounts/route.go:ValidateReservedCapacity":   "pairing.Lock",
		"agentpairing/module.go:in":                         "pairing.Lock",
		"agentpairing/lifecycle.go:Lock":                    "pairing tree tenant:NO KEY UPDATE",
		"authz/accept.go:AcceptInvite":                      "tenant:NO KEY UPDATE advisory:alias",
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
		"authz/project_members.go:authorizeProjectMutation": "project.Mutation",
		"authz/project_members.go:LockProjectMutation":      "tree tenant:UPDATE",
		"authz/project_members.go:LockProjectWrite":         "tree tenant:SHARE",
		"authz/project_members.go:setProjectBindingTx":      "project.Mutation project.Authorize",
		"authz/project_members.go:removeProjectBindingTx":   "project.Mutation project.Authorize",
		"recurrences/module.go:lock":                        "tree:try tree tenant:NO KEY UPDATE",
		"recurrences/module.go:create":                      "recurrence.lock",
		"recurrences/module.go:update":                      "recurrence.lock",
		"recurrences/module.go:setPaused":                   "recurrence.lock",
		"recurrences/engine.go:RunTenant":                   "recurrence.lock",
		"recurrences/engine.go:recordFailure":               "recurrence.lock",
		"recurrences/engine.go:syncPublications":            "recurrence.lock",
		"recurrences/occurrence.go:ensureActor":             "advisory:recurring-actor",
		"recurrences/occurrence.go:runNow":                  "recurrence.lock",
		"operatoractor/actor.go:Ensure":                     "operator.EnsureWithProduction",
		"operatoractor/actor.go:EnsureWithProduction":       "tree tenant:UPDATE",
	}
	got := map[string]string{}
	for _, pkg := range []string{"agentaccounts", "agentpairing", "authz", "recurrences", "operatoractor"} {
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
				var sequence []string
				ast.Inspect(fn.Body, func(n ast.Node) bool {
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
							"agentpairing.Lock": "pairing.Lock", "LockProjectMutation": "project.Mutation",
							"LockProjectWrite": "project.Write", "authz.LockProjectMutation": "project.Mutation",
							"authz.LockProjectWrite": "project.Write", "m.authorizeMutation": "access.Mutation",
							"m.authorizeProjectMutation": "project.Authorize",
						}[name]
						if pkg == "agentpairing" && name == "Lock" {
							label = "pairing.Lock"
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
				if len(sequence) > 0 {
					got[pkg+"/"+filepath.Base(path)+":"+fn.Name.Name] = strings.Join(sequence, " ")
				}
			}
		}
	}
	for site, sequence := range got {
		if expected, ok := want[site]; !ok || sequence != expected {
			t.Errorf("%s: lock sequence %q, want %q (pairing -> tree -> tenant)", site, sequence, expected)
		}
	}
	for site := range want {
		if _, ok := got[site]; !ok {
			t.Errorf("missing lock site %s", site)
		}
	}
	if reflect.DeepEqual(got, want) {
		t.Logf("verified %d lock sites across five packages", len(got))
	}
}
