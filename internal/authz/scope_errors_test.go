// SPDX-License-Identifier: AGPL-3.0-only
package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestScopeLookupPropagatesConnectionFailure(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgres://localhost/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithCancel(tenant.WithPrincipal(t.Context(), tenant.Principal{ID: "10000000-0000-4000-8000-000000000001", TenantID: "10000000-0000-4000-8000-000000000002", Kind: tenant.Person}))
	cancel()
	for _, kind := range []string{"node", "attachment", "relation", "session", "inbox_receipt", "node_key", "event"} {
		id := "10000000-0000-4000-8000-000000000003"
		if kind == "node_key" {
			id = "TEST-1"
		}
		if kind == "event" {
			id = "1"
		}
		if _, err := targetProject(ctx, pool, kind, id); !errors.Is(err, context.Canceled) {
			t.Errorf("%s swallowed connection failure: %v", kind, err)
		}
	}
}
