// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Risk 26: a broad verification bypass allows ordinary use of an account with
// zero cells. Exercise the real pairing, route, reservation and claim paths.
func TestPairingVerificationAccountContextExceptionIsBound(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	e := v.Enrollments[0]
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM account_use_cells WHERE account_id=$1`, e.AccountID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var cells int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM account_use_cells WHERE tenant_id=$1 AND account_id=$2`, f.tenantID, e.AccountID).Scan(&cells); err != nil {
		t.Fatal(err)
	}
	if cells != 0 {
		t.Fatal("fixture kept cells")
	}
	w := f.call("GET", "/api/agent-accounts/use?harness=claude&account_id="+e.AccountID, nil, true, "", 409)
	if !strings.Contains(w.Body.String(), "account_not_allowed_for_context") {
		t.Fatal("wrong use refusal", w.Body.String())
	}
	var verification agentruns.Run
	decodeResult(t, f.call("GET", "/api/runs/"+*e.VerificationRunID, nil, false, key, 200), &verification)
	w = f.call("POST", "/api/work-orders/"+verification.OrderID+"/runs", map[string]string{"agent_principal_id": *v.PrincipalID, "model_profile_id": e.ProfileID, "requested_account_id": e.AccountID}, true, "", 409)
	if !strings.Contains(w.Body.String(), "account_not_allowed_for_context") {
		t.Fatal("ordinary creation borrowed verification", w.Body.String())
	}
	check := routeWithUnits(t, f, v, e, key, verification.ID, map[string]int{"requests": 1}, 200)
	if len(check.Reservations) != 1 {
		t.Fatal("verification didn't reserve", check)
	}
	f.claim(v, e, key, reservationIDs(check), 200)
}
