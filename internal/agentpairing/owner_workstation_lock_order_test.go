// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// Extend main's exact lock inventory to auth without changing its shipped
// helper/guard expectations. Closures inside workstationGuard are inspected
// by the same parser, so a tenant lock before LockMutation cannot hide there.
func TestOwnerWorkstationAuthLockInventory(t *testing.T) {
	want := map[string]string{
		"key_scopes.go:agentKeyScopes":                "tenant:NO KEY UPDATE",
		"key_trim.go:trimFence":                       "tenant:NO KEY UPDATE",
		"owner_workstation.go:workstationGuard":       "pairing.Mutation",
		"owner_workstation.go:auditWorkstation":       "tenant:NO KEY UPDATE",
		"owner_workstation.go:handleOwnerWorkstation": "pairing.Mutation",
		"store.go:resolveOIDCPerson":                  "tenant:NO KEY UPDATE",
		"store.go:issueAgentKeyTx":                    "operator.Ensure tenant:UPDATE",
		"store.go:grantJourneyScopes":                 "operator.Ensure",
		"store.go:revokeAgentKey":                     "tenant:NO KEY UPDATE",
		"store.go:revokeAgentKeyTx":                   "operator.Ensure",
		"store.go:rotateAgentKeyWithScopes":           "tenant:UPDATE",
	}
	files, err := filepath.Glob(filepath.Join("..", "auth", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("inventory auth: files=%d err=%v", len(files), err)
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
			if sequence := lockOrderSequence(t, "auth", fn.Body); len(sequence) > 0 {
				got[filepath.Base(path)+":"+fn.Name.Name] = strings.Join(sequence, " ")
			}
		}
	}
	for site, sequence := range got {
		if expected, ok := want[site]; !ok || sequence != expected {
			t.Errorf("auth/%s: lock sequence %q, want %q (pairing -> tree -> tenant)", site, sequence, expected)
		}
	}
	for site := range want {
		if _, ok := got[site]; !ok {
			t.Errorf("missing lock site auth/%s", site)
		}
	}
}

// Recognize the exact rejected sequences, including closure-nested primitives
// and helpers, so an unrecognized lock cannot make a negative control pass.
func TestWorkstationLockOrderNegativeControls(t *testing.T) {
	tenantSQL := "tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`)"
	pairingSQL := "tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-pairing:'||$1::text,0))`)"
	treeSQL := "tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,0))`)"
	for _, tc := range []struct{ name, ordered, inverted, want, wantInverted string }{
		{"tenant_before_pairing", pairingSQL + "; " + treeSQL + "; " + tenantSQL,
			tenantSQL + "; " + pairingSQL + "; " + treeSQL,
			"pairing tree tenant:NO KEY UPDATE", "tenant:NO KEY UPDATE pairing tree"},
		{"tenant_before_tree", pairingSQL + "; " + treeSQL + "; " + tenantSQL,
			pairingSQL + "; " + tenantSQL + "; " + treeSQL,
			"pairing tree tenant:NO KEY UPDATE", "pairing tenant:NO KEY UPDATE tree"},
		{"helper", "agentpairing.LockMutation(ctx, tx)",
			tenantSQL + "; agentpairing.LockMutation(ctx, tx)",
			"pairing.Mutation", "tenant:NO KEY UPDATE pairing.Mutation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sequence := func(body string) string {
				file, err := parser.ParseFile(token.NewFileSet(), "guard.go",
					"package auth; func workstationGuard() { return func() { "+body+" } }", 0)
				if err != nil {
					t.Fatal(err)
				}
				return strings.Join(lockOrderSequence(t, "auth", file.Decls[0].(*ast.FuncDecl).Body), " ")
			}
			if got := sequence(tc.ordered); got != tc.want {
				t.Fatalf("ordered closure lock sequence %q, want %q", got, tc.want)
			}
			if got := sequence(tc.inverted); got != tc.wantInverted || got == tc.want {
				t.Fatalf("inverted closure lock sequence %q, want rejected sequence %q", got, tc.wantInverted)
			}
		})
	}
}
