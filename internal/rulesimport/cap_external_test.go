// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport_test

import (
	"testing"

	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rulesimport"
)

// An external test package: internal/rules imports rulesimport through the
// doctrine layer (AEON-318), so an in-package test importing rules would cycle.
func TestAlwaysOnBudgetMatchesShippedCap(t *testing.T) {
	if rulesimport.AlwaysOnBudget != rules.MaxBytes {
		t.Fatalf("importer budget %d, shipped cap %d", rulesimport.AlwaysOnBudget, rules.MaxBytes)
	}
}
