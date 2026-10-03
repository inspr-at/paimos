// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"fmt"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

func TestWatchRecoveryBudgetBoundsIdentityMemoryAndRetry(t *testing.T) {
	var limits watchRecoveryLimits
	now := time.Unix(1_700_000_000, 0)
	p := tenant.Principal{TenantID: "tenant", ID: "computer"}
	for i := 0; i < recoveryAttempts; i++ {
		if err := limits.limit(p, now); err != nil {
			t.Fatal("recovery slot refused", err)
		}
	}
	capped := limits.limit(p, now.Add(1250*time.Millisecond))
	w := httptest.NewRecorder()
	WriteError(w, capped)
	if w.Code != 429 || w.Header().Get("Retry-After") != "59" || limits.computers["tenant/computer"].n != recoveryAttempts {
		t.Fatal("cap lost its exact retry delay or increased an exhausted counter")
	}
	// Identical principal IDs in distinct tenants never share a budget.
	other := tenant.Principal{TenantID: "other", ID: p.ID}
	if err := limits.limit(other, now); err != nil {
		t.Fatal("tenant budgets coupled", err)
	}
	for i := len(limits.computers); i < recoveryCapacity; i++ {
		if err := limits.limit(tenant.Principal{TenantID: "tenant", ID: fmt.Sprint(i)}, now); err != nil {
			t.Fatal("capacity slot refused", err)
		}
	}
	w = httptest.NewRecorder()
	WriteError(w, limits.limit(tenant.Principal{TenantID: "tenant", ID: "overflow"}, now))
	retry, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if w.Code != 429 || err != nil || retry != 60 || len(limits.computers) != recoveryCapacity {
		t.Fatal("recovery map grew beyond its bound or lost capacity retry guidance")
	}
	// Exhausting the map never denies a retained computer with slots left.
	if err := limits.limit(other, now); err != nil {
		t.Fatal("full map refused an existing computer", err)
	}
	if err := limits.limit(p, now.Add(recoveryWindow)); err != nil || len(limits.computers) != 1 || limits.computers["tenant/computer"].n != 1 {
		t.Fatal("expired budgets did not release capacity and reset exactly at the boundary", err)
	}
}
