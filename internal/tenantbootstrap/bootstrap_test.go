// SPDX-License-Identifier: AGPL-3.0-only

package tenantbootstrap

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestCreateBindAndTenantIsolation(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	a, err := Create(ctx, d.App, "inspr", "INSPR")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Create(ctx, d.App, "example-business", "Example Business")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("tenants share an ID")
	}
	for slug, id := range map[string]string{"inspr": a, "example-business": b} {
		got, err := ResolveSlug(ctx, d.App, slug)
		if err != nil || got != id {
			t.Fatalf("resolve %s: %s %v", slug, got, err)
		}
		var kinds, created int
		if err := db.InTenant(dbtest.Seed(ctx), d.App, id, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM node_kinds`).Scan(&kinds); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='tenant.created'`).Scan(&created)
		}); err != nil {
			t.Fatal(err)
		}
		if kinds < 8 || created != 1 {
			t.Fatalf("%s kinds=%d events=%d", slug, kinds, created)
		}
	}
	first, err := BindOIDC(ctx, d.App, "example-business", "https://issuer.example", "operator", "Operator", "admin")
	if err != nil {
		t.Fatal(err)
	}
	second, err := BindOIDC(ctx, d.App, "inspr", "https://issuer.example", "operator", "Operator", "member")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("shared OIDC identity merged principals")
	}
	if again, err := BindOIDC(ctx, d.App, "example-business", "https://issuer.example", "operator", "Operator", "admin"); err != nil || again != first {
		t.Fatalf("replay: %s %v", again, err)
	}
	var bound int
	if err := db.InTenant(dbtest.Seed(ctx), d.App, b, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='tenant.principal_bound'`).Scan(&bound)
	}); err != nil {
		t.Fatal(err)
	}
	if bound != 1 {
		t.Fatalf("replay added event: %d", bound)
	}
	if _, err := BindOIDC(ctx, d.App, "example-business", "https://issuer.example", "customer", "Customer", "customer"); err != nil {
		t.Fatal(err)
	}
	var actor string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, b, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT actor_principal_id::text FROM events
			WHERE type='tenant.principal_bound' ORDER BY id DESC LIMIT 1`).Scan(&actor)
	}); err != nil {
		t.Fatal(err)
	}
	if actor != first {
		t.Fatalf("customer binding actor %s, want operator %s", actor, first)
	}
	if _, err := Create(ctx, d.App, "example-business", "Duplicate"); err == nil {
		t.Fatal("duplicate slug succeeded")
	}
}
