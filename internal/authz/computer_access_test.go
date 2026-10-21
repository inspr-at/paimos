// SPDX-License-Identifier: AGPL-3.0-only
package authz

import (
	"slices"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: generic run defaults or an agent/workstation key silently open every
// computer, or project bindings acquire this workspace/computer-only authority.
func TestComputerStartPermissionIsPersonOnlyAndScopeLimited(t *testing.T) {
	p, ok := Lookup(StartOnComputer)
	if !ok || !slices.Equal(p.GrantableAt, []string{"workspace", "computer"}) || p.AgentGrantable || p.OwnerWorkstationGrantable || ProjectGrantable(p.Key) || !GrantableAt(p.Key, "computer") {
		t.Fatalf("computer permission metadata drifted: %+v", p)
	}
	for _, role := range builtinKeys {
		if contains(builtinPermissions(role), StartOnComputer) != (role == "owner" || role == "admin") {
			t.Fatalf("implicit computer access changed for %s", role)
		}
	}
	for _, workstation := range []bool{false, true} {
		if KeyGrantable(StartOnComputer, workstation) {
			t.Fatal("computer start escaped the person-only key ceiling")
		}
	}
	// A non-person is refused before any database lookup, even if its role or
	// scopes happened to contain this permission.
	allowed, err := CanStartOnComputerTx(t.Context(), nil, tenant.Principal{Kind: tenant.Agent, ID: "daemon"}, "computer")
	if allowed || err != nil {
		t.Fatalf("agent start permission: %v, %v", allowed, err)
	}
}
