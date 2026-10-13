// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"errors"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/jackc/pgx/v5"
)

// Risk: turn completion/viewer loss/daemon replacement must not make native
// roots free capacity, and worker run reservations must never count twice.
func TestNativeRootHoldsCountUntilVerifiedRelease(t *testing.T) {
	d := dbtest.Open(t)
	const tenantID = "8c372034-611b-48d3-8e12-b7cb25f8c737"
	const session = "01900000-0000-4000-8000-000000000001"
	const epoch = "01900000-0000-4000-8000-000000000002"
	const account = "01900000-0000-4000-8000-000000000003"
	const computer = "01900000-0000-4000-8000-000000000004"
	ctx := dbtest.Seed(t.Context())
	err := db.InTenant(ctx, d.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,slug,name) VALUES($1,'native-root','native root')`, tenantID); err != nil {
			return err
		}
		admit := NativeAdmission{TenantID: tenantID, AccountID: account, ComputerID: computer, ProjectID: "01900000-0000-4000-8000-000000000005", OwnerPersonID: "01900000-0000-4000-8000-000000000006"}
		// This fixture exercises occupancy only; claim behavior tests supply
		// the real admitted quota budget to ReserveNativeRootTx.
		var root *string
		var err error
		if err := tx.QueryRow(ctx, `INSERT INTO native_session_roots(tenant_id,id,owned_session_id,project_id,owner_person_id,account_id,computer_id,runtime_epoch,hold_state)
 VALUES($1,gen_random_uuid(),$2,$3,$4,$5,$6,$7,'starting') RETURNING id::text`, tenantID, session, admit.ProjectID, admit.OwnerPersonID, account, computer, epoch).Scan(&root); err != nil {
			return err
		}
		if root == nil {
			t.Fatal("root reservation missing")
		}
		for _, state := range []string{"starting", "running", "idle", "taken", "exit_unconfirmed"} {
			if err = SetNativeRootStateTx(ctx, tx, session, epoch, state); err != nil {
				return err
			}
			n, err := NativeRootCountTx(ctx, tx, account, computer, "")
			if err != nil {
				return err
			}
			if n != 1 {
				t.Fatalf("%s freed root: %d", state, n)
			}
		}
		run := "01900000-0000-4000-8000-000000000007"
		admit.RunID = &run
		worker, err := ReserveNativeRootTx(ctx, tx, admit, "01900000-0000-4000-8000-000000000008", epoch, NativeBudget{})
		if err != nil {
			return err
		}
		if worker != nil {
			t.Fatal("worker received a second reservation")
		}
		var reason *nativecontract.ValidationError
		if err = SetNativeRootStateTx(ctx, tx, session, run, "idle"); !errors.As(err, &reason) || reason.Reason != nativecontract.ReasonWrongGeneration {
			t.Fatalf("stale epoch: %v", err)
		}
		n, err := NativeRootCountTx(ctx, tx, account, computer, session)
		if err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("own root not excluded from revalidation")
		}
		// A forged sidecar with a worker run must not add a root slot either.
		if _, err = tx.Exec(ctx, `INSERT INTO native_session_roots(tenant_id,id,owned_session_id,account_id,computer_id,run_id,hold_state) VALUES($1,gen_random_uuid(),gen_random_uuid(),$2,$3,$4,'idle')`, tenantID, account, computer, run); err != nil {
			return err
		}
		n, err = NativeRootCountTx(ctx, tx, account, computer, "")
		if err != nil {
			return err
		}
		if n != 1 {
			t.Fatal("worker sidecar counted twice")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
