// SPDX-License-Identifier: AGPL-3.0-only
package dsar

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestInventoryAndAdapters(t *testing.T) {
	domains, err := Inventory()
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) < 100 {
		t.Fatal("incomplete domain inventory")
	}
	byTable := make(map[string]Domain)
	for _, d := range domains {
		byTable[d.Table] = d
	}
	for _, d := range domains {
		for _, erase := range []bool{false, true} {
			q := adapterQuery(d, byTable, erase)
			if strings.Contains(q, "SELECT *") || strings.Contains(q, "to_jsonb(t)") {
				t.Fatalf("unbounded projection in %s", d.Table)
			}
			for _, c := range strings.Fields(d.Secret) {
				if strings.Contains(q, pgx.Identifier{"t", c}.Sanitize()) {
					t.Fatalf("credential column queried: %s.%s", d.Table, c)
				}
			}
			if d.Table != "identities" && !strings.Contains(q, "t.tenant_id=$1::uuid") {
				t.Fatalf("unscoped adapter %s", d.Table)
			}
		}
	}
	if contains(byTable["sessions"].Export, "id") || contains(byTable["agent_keys"].Export, "hash") {
		t.Fatal("authentication material exported")
	}
	if !contains(byTable["nodes"].Review, "fields") || !contains(byTable["events"].Review, "after") || !contains(byTable["personal_profiles"].Review, "avatar_hashes") {
		t.Fatal("personal document inventory incomplete")
	}
	bad := append([]Domain{}, domains...)
	bad[0].Export += " email"
	bad[0].Secret += " email"
	if validateInventory(bad) == nil {
		t.Fatal("conflicting classifications accepted")
	}
}

// Every migrated column is classified, a stronger gate than an email/name
// heuristic. Changes with unrelated names must register too.
func TestMigratedInventoryGuard(t *testing.T) {
	database := dbtest.Open(t)
	domains, err := Inventory()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.App.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if err := CheckSchema(t.Context(), tx, domains); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"dsar_test_email", "dsar_test_name", "dsar_test_display_name", "dsar_test_user_id", "dsar_test_person_id", "opaque_new_column"} {
		if _, err := tx.Exec(t.Context(), "ALTER TABLE principals ADD COLUMN "+pgx.Identifier{col}.Sanitize()+" text"); err != nil {
			t.Fatal(err)
		}
		if err := CheckSchema(t.Context(), tx, domains); err == nil || !strings.Contains(err.Error(), "principals."+col) {
			t.Fatalf("new column did not fail guard: %s (%v)", col, err)
		}
	}
	if _, err := tx.Exec(t.Context(), `CREATE TABLE dsar_new_domain (tenant_id uuid, email text)`); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(t.Context(), tx, domains); err == nil || !strings.Contains(err.Error(), "dsar_new_domain.email") {
		t.Fatalf("new table did not fail guard: %v", err)
	}
}
