// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"slices"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
)

// AEON-367: a key may hold more than 32 scopes; the registry and the role and
// creator ceilings bound it, and only a raw input guard remains.
func TestCleanScopesHasNoFixedCap(t *testing.T) {
	var grantable []string
	for _, p := range authz.Registry {
		if p.AgentGrantable {
			grantable = append(grantable, p.Key)
		}
	}
	if len(grantable) <= 32 {
		t.Skipf("registry has only %d agent-grantable permissions", len(grantable))
	}
	out, err := cleanScopes(grantable)
	if err != nil || len(out) != len(grantable) {
		t.Fatalf("cleanScopes(%d grantable) = %d, %v", len(grantable), len(out), err)
	}
	dup := append(slices.Clone(grantable), grantable[0], grantable[1])
	if out, err := cleanScopes(dup); err != nil || len(out) != len(grantable) {
		t.Fatalf("duplicates not folded: %d, %v", len(out), err)
	}
	raw := make([]string, maxScopeInput+1)
	for i := range raw {
		raw[i] = grantable[i%len(grantable)]
	}
	if _, err := cleanScopes(raw); err == nil {
		t.Fatal("raw input above the guard was accepted")
	}
	if _, err := cleanScopes([]string{"keys.manage"}); err == nil {
		t.Fatal("non-grantable scope accepted")
	}
	if _, err := cleanScopes([]string{"unknown.scope"}); err == nil {
		t.Fatal("unknown scope accepted")
	}
}
