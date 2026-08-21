// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/httpcontract"
)

func TestParseScopesFailsClosedOnEmptyStorage(t *testing.T) {
	for _, stored := range []string{"", " ", "\t", ",", " , , "} {
		t.Run(stored, func(t *testing.T) {
			scopes := ParseScopes(stored)
			if len(scopes) != 0 || scopes.Has(ScopeAll) || scopes.Has("projects:write") {
				t.Fatalf("ParseScopes(%q) granted authority: %#v", stored, scopes)
			}
		})
	}

	all := ParseScopes(ScopeAll)
	if len(all) != 1 || !all.Has(ScopeAll) || !all.Has("projects:write") {
		t.Fatalf("explicit all-scope sentinel was not preserved: %#v", all)
	}
}

// PAI-809 — the control scopes exist under exactly these names. A rename
// or a second spelling would leave keys minted against the old name
// silently unauthorized, so the literals are pinned here rather than
// referenced through the constants alone.
func TestAgentControlScopeNamesAreExact(t *testing.T) {
	if ScopeAgentControlsWrite != "agent-controls:write" {
		t.Fatalf("write scope renamed: %q", ScopeAgentControlsWrite)
	}
	if ScopeAgentControlsRunner != "agent-controls:runner" {
		t.Fatalf("runner scope renamed: %q", ScopeAgentControlsRunner)
	}
	catalog := map[string]ScopeDef{}
	for _, def := range ScopeCatalog() {
		catalog[def.Name] = def
	}
	for _, name := range []string{ScopeAgentControlsWrite, ScopeAgentControlsRunner} {
		def, ok := catalog[name]
		if !ok {
			t.Fatalf("scope %q missing from the catalog", name)
		}
		if def.RequiredRole != RoleMember {
			t.Fatalf("scope %q minimum role is %q, want member", name, def.RequiredRole)
		}
		if len(def.Endpoints) == 0 {
			t.Fatalf("scope %q advertises no endpoints", name)
		}
		// The advertised families must be families the path classifier
		// actually recognizes — otherwise the catalog and the privacy
		// classifier could drift into disagreeing about what "control"
		// means.
		for _, endpoint := range def.Endpoints {
			concrete := endpoint
			for _, param := range []string{"{deliveryKey}", "{id}"} {
				concrete = strings.ReplaceAll(concrete, param, "sample")
			}
			if _, ok := httpcontract.ClassifyControlPath(concrete); !ok {
				t.Fatalf("scope %q advertises %q, which is not a classified control route", name, endpoint)
			}
		}
	}
}

// The catalog's role table, stated as a table. Internal roles satisfy the
// member minimum; only admin and super_admin satisfy the admin minimum;
// external and anything unrecognized satisfy nothing.
func TestScopeAdmissionRoleTable(t *testing.T) {
	cases := []struct {
		scope string
		role  string
		admit bool
	}{
		{ScopeAgentControlsWrite, RoleMember, true},
		{ScopeAgentControlsWrite, RoleAdmin, true},
		{ScopeAgentControlsWrite, RoleSuperAdmin, true},
		{ScopeAgentControlsWrite, RoleExternal, false},
		{ScopeAgentControlsWrite, "", false},
		{ScopeAgentControlsWrite, "owner", false},
		{ScopeAgentControlsWrite, "Member", false},
		{ScopeAgentControlsRunner, RoleMember, true},
		{ScopeAgentControlsRunner, RoleAdmin, true},
		{ScopeAgentControlsRunner, RoleSuperAdmin, true},
		{ScopeAgentControlsRunner, RoleExternal, false},
		{ScopeAgentControlsRunner, "unknown", false},
		{ScopeProjectsWrite, RoleAdmin, true},
		{ScopeProjectsWrite, RoleSuperAdmin, true},
		{ScopeProjectsWrite, RoleMember, false},
		{ScopeProjectsWrite, RoleExternal, false},
		// Unknown scope names fail closed for every role, including
		// near-misses of the two new names.
		{"agent-controls:*", RoleSuperAdmin, false},
		{"agent-controls", RoleSuperAdmin, false},
		{"agent-controls:write ", RoleAdmin, false},
		{"AGENT-CONTROLS:WRITE", RoleAdmin, false},
		{"agent-controls:writer", RoleAdmin, false},
	}
	for _, tc := range cases {
		t.Run(tc.scope+"/"+tc.role, func(t *testing.T) {
			err := ValidateScopesForRole(ScopeSet{tc.scope: {}}, tc.role)
			if tc.admit && err != nil {
				t.Fatalf("role %q was refused scope %q: %v", tc.role, tc.scope, err)
			}
			if !tc.admit && err == nil {
				t.Fatalf("role %q was allowed to attach scope %q", tc.role, tc.scope)
			}
		})
	}
	// The sentinel is a no-op narrowing and stays allowed for everyone
	// the middleware would let through in the first place.
	if err := ValidateScopesForRole(ScopeSet{ScopeAll: {}}, RoleMember); err != nil {
		t.Fatalf("member was refused the all-scope sentinel: %v", err)
	}
}

// API keys admit a control scope only on an exact name or the sentinel;
// browser sessions keep wildcard semantics; a persisted empty column
// still means no access at all.
func TestControlScopeSetParity(t *testing.T) {
	named := ParseScopes(ScopeAgentControlsRunner)
	if !named.Has(ScopeAgentControlsRunner) {
		t.Fatal("named control scope did not admit itself")
	}
	if named.Has(ScopeAgentControlsWrite) || named.Has(ScopeProjectsWrite) || named.Has(ScopeAll) {
		t.Fatalf("named control scope widened beyond itself: %#v", named)
	}

	both := ParseScopes(ScopeAgentControlsWrite + " , " + ScopeAgentControlsRunner)
	if !both.Has(ScopeAgentControlsWrite) || !both.Has(ScopeAgentControlsRunner) {
		t.Fatalf("csv parity lost a named control scope: %#v", both)
	}

	wildcard := ParseScopes(ScopeAll)
	if !wildcard.Has(ScopeAgentControlsWrite) || !wildcard.Has(ScopeAgentControlsRunner) {
		t.Fatal("explicit sentinel did not admit the control scopes")
	}

	for _, stored := range []string{"", "   ", ",", " , , "} {
		empty := ParseScopes(stored)
		if empty.Has(ScopeAgentControlsWrite) || empty.Has(ScopeAgentControlsRunner) {
			t.Fatalf("empty scope column %q admitted a control scope", stored)
		}
	}

	// The session branch of Middleware attaches this exact set.
	session := ScopeSet{ScopeAll: {}}
	if !session.Has(ScopeAgentControlsWrite) || !session.Has(ScopeAgentControlsRunner) {
		t.Fatal("session wildcard no longer admits the control scopes")
	}
}

// A control scope removed after the middleware ran must not survive into
// the transaction that actually applies the command.
func TestControlScopeReauthorizationReadsCurrentColumn(t *testing.T) {
	setupPrincipalTestDB(t)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	userID := insertPrincipalUser(t, "control-scope-owner")
	rawKey := "paimos_test_control-scope-secret"
	digest := sha256.Sum256([]byte(rawKey))
	result, err := db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'control-scope',?,'paimos_test',?)`,
		userID, hex.EncodeToString(digest[:]), ScopeAgentControlsRunner)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := result.LastInsertId()
	middlewarePrincipal, err := NewAPIKeyPrincipal(keyID, userID, ScopeSet{ScopeAgentControlsRunner: {}})
	if err != nil {
		t.Fatal(err)
	}

	reauthorize := func(t *testing.T) Principal {
		t.Helper()
		tx, err := db.DB.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		_, current, err := ReauthorizePrincipalTx(context.Background(), tx, middlewarePrincipal, now)
		if err != nil {
			t.Fatalf("reauthorize: %v", err)
		}
		return current
	}

	current := reauthorize(t)
	if !current.HasScope(ScopeAgentControlsRunner) || current.HasScope(ScopeAgentControlsWrite) {
		t.Fatalf("reauthorized principal has the wrong scopes: %#v", current.Scopes())
	}

	// Scope removed between middleware and transaction: fail closed.
	if _, err := db.DB.Exec(`UPDATE api_keys SET scopes='' WHERE id=?`, keyID); err != nil {
		t.Fatal(err)
	}
	if stripped := reauthorize(t); stripped.HasScope(ScopeAgentControlsRunner) || stripped.HasScope(ScopeAll) {
		t.Fatalf("removed scope survived reauthorization: %#v", stripped.Scopes())
	}

	// Widened to the sentinel between middleware and transaction: the
	// current column wins there too.
	if _, err := db.DB.Exec(`UPDATE api_keys SET scopes=? WHERE id=?`, ScopeAll, keyID); err != nil {
		t.Fatal(err)
	}
	if widened := reauthorize(t); !widened.HasScope(ScopeAgentControlsRunner) || !widened.HasScope(ScopeAgentControlsWrite) {
		t.Fatalf("sentinel column did not reauthorize: %#v", widened.Scopes())
	}
}
