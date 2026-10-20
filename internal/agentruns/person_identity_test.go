// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"errors"
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: preferences/requester provenance invents an actor, or a retry rewrites
// the person a run acts for after admission.
func TestRunActingPersonIsExplicitAndWriteOnce(t *testing.T) {
	f := setup(t)
	run := f.run(t, f.order(t, nil))
	if run.ActingPersonID != nil {
		t.Fatal("new run inferred an acting person")
	}
	var otherPerson string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Other actor') RETURNING id::text`, f.person.TenantID).Scan(&otherPerson)
	})
	bind := func(person string) error {
		return db.InTenant(tenant.WithPrincipal(t.Context(), f.person), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
			if err := db.LockTenant(t.Context(), tx, f.person.TenantID); err != nil {
				return err
			}
			return agentruns.SetActingPersonTx(t.Context(), tx, f.person.TenantID, run.ID, person)
		})
	}
	for _, person := range []string{f.agent.ID, f.foreign.ID} {
		if err := bind(person); !errors.Is(err, agentruns.ErrActingPersonBinding) {
			t.Fatalf("invalid actor: %v", err)
		}
	}
	if err := bind(f.person.ID); err != nil {
		t.Fatal(err)
	}
	if err := bind(f.person.ID); err != nil {
		t.Fatalf("same actor replay: %v", err)
	}
	if err := bind(otherPerson); !errors.Is(err, agentruns.ErrActingPersonBinding) {
		t.Fatalf("changed actor: %v", err)
	}
	var got agentruns.Run
	f.call(t, f.person, "GET", "/api/runs/"+run.ID, nil, 200, &got)
	if got.ActingPersonID == nil || *got.ActingPersonID != f.person.ID {
		t.Fatalf("actor response lost binding: %+v", got.ActingPersonID)
	}
}
