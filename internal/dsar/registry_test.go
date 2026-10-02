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
			if d.Table != "identities" && d.Table != "tenants" && !strings.Contains(q, "t.tenant_id=$1::uuid") {
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
	deprovisioned := byTable["aithema_deprovisioned_subjects"]
	if !contains(deprovisioned.Review, "subject") || !contains(deprovisioned.Locator, "issuer") || !contains(deprovisioned.Locator, "subject") {
		t.Fatal("OIDC subject needs personal classification and a logical issuer/subject locator")
	}
	if !contains(byTable["method_learning_decisions"].Review, "decided_by") || !contains(byTable["method_learning_decisions"].Subjects, "decided_by") {
		t.Fatal("method learning decision actor is personal data")
	}
	bad := append([]Domain{}, domains...)
	bad[0].Export += " email"
	bad[0].Secret += " email"
	if validateInventory(bad) == nil {
		t.Fatal("conflicting classifications accepted")
	}
}

func TestInventoryRejectsPersonalMetadata(t *testing.T) {
	for _, col := range []string{"email", "name", "display_name", "user_id", "person_id", "phone", "subject", "oidc_subject", "external_account_id", "account_key", "principal_id", "created_by_principal_id", "decided_by", "session_id", "client_session_id", "sid"} {
		t.Run(col, func(t *testing.T) {
			d := Domain{Table: "new_domain", Metadata: "tenant_id " + col, Hold: "none"}
			if err := validateInventory([]Domain{d}); err == nil || !strings.Contains(err.Error(), "new_domain."+col) {
				t.Fatalf("personal metadata accepted: %v", err)
			}
		})
	}
	// Even an opaque name cannot be declared non-personal when its adapter
	// explicitly uses that column to identify the person.
	if validateInventory([]Domain{{Table: "new_domain", Metadata: "tenant_id opaque", Subjects: "opaque", Hold: "none"}}) == nil {
		t.Fatal("opaque subject selector accepted as metadata")
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
	for _, col := range []string{"dsar_test_email", "dsar_test_name", "dsar_test_display_name", "dsar_test_user_id", "dsar_test_person_id", "dsar_test_phone", "dsar_test_subject", "dsar_test_external_account_id", "opaque_new_column"} {
		if _, err := tx.Exec(t.Context(), "ALTER TABLE principals ADD COLUMN "+pgx.Identifier{col}.Sanitize()+" text"); err != nil {
			t.Fatal(err)
		}
		if err := CheckSchema(t.Context(), tx, domains); err == nil || !strings.Contains(err.Error(), "principals."+col) {
			t.Fatalf("new column did not fail guard: %s (%v)", col, err)
		}
		if personalColumn.MatchString(col) {
			misclassified := append([]Domain{}, domains...)
			for i := range misclassified {
				if misclassified[i].Table == "principals" {
					misclassified[i].Metadata += " " + col
				}
			}
			if err := CheckSchema(t.Context(), tx, misclassified); err == nil || !strings.Contains(err.Error(), "personal identifier") {
				t.Fatalf("registering personal column as metadata bypassed guard: %s (%v)", col, err)
			}
		}
	}
	if _, err := tx.Exec(t.Context(), `CREATE TABLE dsar_new_domain (tenant_id uuid, email text)`); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(t.Context(), tx, domains); err == nil || !strings.Contains(err.Error(), "dsar_new_domain.email") {
		t.Fatalf("new table did not fail guard: %v", err)
	}
}
