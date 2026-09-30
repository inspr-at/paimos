// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

// AEON-449: a session carries its own revision, bumped by the statement that changes
// the row. A client that holds two copies keeps the one with the larger row_version,
// so the version has to grow in the same transaction as the change: never behind it,
// never after it, never on a rolled-back attempt.
func TestSessionRowVersionGrowsInTheTransactionThatChangesTheRow(t *testing.T) {
	f := fixture(t)
	path := "/api/projects/" + f.project + "/harness-sessions"
	registered := decode(t, f.call(f.person, "POST", path, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test-host", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": "rv-" + uid(), "worker_lease": "rv-lease-" + uid()}, ""))
	id := registered["id"].(string)
	version := func(tx pgx.Tx) (v int64) {
		t.Helper()
		if err := tx.QueryRow(t.Context(), `SELECT row_version FROM harness_sessions WHERE id=$1`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	// Registration touches the row a few times after it is created (the owner, the
	// client report): the body names the row it was made from, never a later one.
	var base int64
	f.tx(t, f.person, func(tx pgx.Tx) error { base = version(tx); return nil })
	if got, _ := registered["row_version"].(float64); got < 1 || int64(got) > base {
		t.Fatalf("a registration result names a version of the row: %v, committed %d", registered["row_version"], base)
	}

	// Two changes in one transaction read +1 then +2 before anything commits, and a
	// rollback takes both back: the version lives and dies with the change.
	rollback := errors.New("rollback")
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET heartbeat_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return err
		}
		if got := version(tx); got != base+1 {
			t.Errorf("after the first change inside the transaction: %d want %d", got, base+1)
		}
		if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET activity='busy' WHERE id=$1`, id); err != nil {
			return err
		}
		if got := version(tx); got != base+2 {
			t.Errorf("after the second change inside the transaction: %d want %d", got, base+2)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("the transaction should have rolled back: %v", err)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if got := version(tx); got != base {
			t.Errorf("a rolled-back change leaves the version: %d want %d", got, base)
		}
		return nil
	})

	// A change no writer has to remember: a bare column update, not through the module.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET heartbeat_at=clock_timestamp() WHERE id=$1`, id)
		return err
	})
	detail := decode(t, f.call(f.person, "GET", path+"/"+id, nil, ""))
	if detail["row_version"] != float64(base+1) {
		t.Fatalf("detail names the committed version: %v want %d", detail["row_version"], base+1)
	}

	// Every read and every mutation result names the row's version, and it only grows.
	removed := decode(t, f.call(f.person, "POST", path+"/"+id+"/remove", map[string]any{"reason": "Clean up"}, ""))
	session := removed["session"].(map[string]any)
	if after, _ := session["row_version"].(float64); after <= float64(base+1) {
		t.Fatalf("a removal result is newer than the detail it follows: %v", session["row_version"])
	}
	list := decode(t, f.call(f.person, "GET", "/api/harness-sessions?view=all", nil, ""))
	var listed float64
	for _, item := range list["items"].([]any) {
		if row := item.(map[string]any); row["id"] == id {
			listed, _ = row["row_version"].(float64)
		}
	}
	if listed != session["row_version"] {
		t.Fatalf("the list names the version the removal result named: list %v, removal %v", listed, session["row_version"])
	}
}
