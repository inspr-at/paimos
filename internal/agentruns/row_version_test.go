// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

// AEON-449: a run carries its own revision, bumped by the statement that changes the
// row. Every body that names a run (the launch result, a read, a claim, a cancel)
// names the version of the row it was made from, so a client that holds two of them
// keeps the larger, whatever order they arrive in.
func TestRunRowVersionGrowsInTheTransactionThatChangesTheRow(t *testing.T) {
	f := setup(t)
	queued := f.run(t, f.order(t, nil))
	if queued.RowVersion < 1 {
		t.Fatalf("a launch result names its version: %d", queued.RowVersion)
	}
	version := func(tx pgx.Tx) (v int64) {
		t.Helper()
		if err := tx.QueryRow(t.Context(), `SELECT row_version FROM agent_runs WHERE id=$1`, queued.ID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	// The version lives and dies with the change: two updates read +1 and +2 inside
	// the transaction, a rollback takes both back, and no writer has to remember it.
	rollback := errors.New("rollback")
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		for step := int64(1); step <= 2; step++ {
			if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET input_tokens=input_tokens+1 WHERE id=$1`, queued.ID); err != nil {
				return err
			}
			if got := version(tx); got != queued.RowVersion+step {
				t.Errorf("after change %d inside the transaction: %d want %d", step, got, queued.RowVersion+step)
			}
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("the transaction should have rolled back: %v", err)
	}
	var read agentruns.Run
	f.call(t, f.person, "GET", "/api/runs/"+queued.ID, nil, 200, &read)
	if read.RowVersion != queued.RowVersion {
		t.Fatalf("a rolled-back change leaves the version: %d want %d", read.RowVersion, queued.RowVersion)
	}

	// A claim changes the row: its result is newer than the launch, and a read agrees.
	claimed := f.claim(t, queued)
	if claimed.RowVersion <= queued.RowVersion {
		t.Fatalf("the claim result is newer than the launch result: %d then %d", queued.RowVersion, claimed.RowVersion)
	}
	f.call(t, f.person, "GET", "/api/runs/"+queued.ID, nil, 200, &read)
	if read.RowVersion != claimed.RowVersion || read.Status != claimed.Status {
		t.Fatalf("a read names the claimed row: %+v", read)
	}
	var page struct {
		Items []agentruns.Run `json:"items"`
	}
	f.call(t, f.person, "GET", "/api/runs?agent="+f.agent.ID, nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].RowVersion != claimed.RowVersion {
		t.Fatalf("the list names the same version: %+v", page.Items)
	}

	// A mutation result that changes the row, here a cancel, is newer than the read before it.
	other := f.run(t, f.order(t, nil))
	f.call(t, f.person, "GET", "/api/runs/"+other.ID, nil, 200, &read)
	var cancelled agentruns.Run
	f.call(t, f.person, "POST", "/api/runs/"+other.ID+"/cancel", nil, 200, &cancelled)
	if cancelled.Status != "cancelled" || cancelled.RowVersion <= read.RowVersion {
		t.Fatalf("a cancel result is newer than the read it follows: %d then %d (%s)", read.RowVersion, cancelled.RowVersion, cancelled.Status)
	}
}
