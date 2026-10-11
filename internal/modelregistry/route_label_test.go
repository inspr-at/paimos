// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestModelKeyMatchesAliasesAndAPIIDs(t *testing.T) {
	for model, want := range map[string]string{
		"opus": "opus", "claude-opus-5-5": "opus", "anthropic/claude-opus-5": "opus", "Claude-Sonnet-5": "sonnet",
		"haiku": "haiku", "gpt-6-astra": "gpt-6-astra", " GPT-6-Sol ": "gpt-6-sol", "grok-4.7-xhigh": "grok-4.7-xhigh",
	} {
		if got := ModelKey(model); got != want {
			t.Errorf("%q: %q, want %q", model, got, want)
		}
	}
}

// The revision is empty before the registry is seeded and changes whenever
// the Models state a route resolves through changes, so a list can name what
// it resolved against. It digests content, not row ids: shared planning
// fixtures captured in one database must match the next run's tenants.
func TestRevisionFollowsRoutes(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "route-revision", "person", "lead", []string{"admin"})
	twin := makePrincipal(t, "route-revision-twin", "person", "lead", []string{"admin"})
	revision := func(p tenant.Principal) string {
		t.Helper()
		var out string
		err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
			var err error
			out, err = Revision(t.Context(), tx)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := revision(p); got != "" {
		t.Fatalf("unseeded revision %q", got)
	}
	for _, who := range []tenant.Principal{p, twin} {
		if code, body := call(t, &who, http.MethodGet, "/api/models", ""); code != http.StatusOK {
			t.Fatalf("seed: %d %s", code, body)
		}
	}
	for _, who := range []tenant.Principal{p, twin} {
		if got := revision(who); got != "" {
			t.Fatalf("ID-only catalog created routing revision %q", got)
		}
		inRegistry(t, who, func(tx pgx.Tx) error { return ensureCatalog(t.Context(), tx, who) })
	}
	seeded := revision(p)
	if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(seeded) || revision(p) != seeded {
		t.Fatalf("seeded revision %q", seeded)
	}
	if revision(twin) != seeded {
		t.Fatalf("equal Models state named differently: %q and %q", seeded, revision(twin))
	}
	change := func(who tenant.Principal, sql string) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, who.TenantID, func(tx pgx.Tx) error {
			var opus string
			if err := tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE slug='claude-opus-xhigh' AND version=$1`, CatalogVersion).Scan(&opus); err != nil {
				return err
			}
			if sql == "" {
				return dbtest.ModelsOrder(t.Context(), tx, who.TenantID, "other", "first", "wait", opus)
			}
			_, err := tx.Exec(t.Context(), sql)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	ordered := ""
	for _, who := range []tenant.Principal{p, twin} {
		change(who, "")
		if ordered == "" {
			ordered = revision(who)
		}
		if got := revision(who); got == seeded || got != ordered {
			t.Fatalf("revision did not follow the Default order: %q → %q (twin %q)", seeded, got, ordered)
		}
	}
	change(p, `INSERT INTO model_pauses(tenant_id,harness,model,reason) SELECT tenant_id,harness,model,'paused' FROM model_profiles WHERE slug='claude-opus-xhigh' LIMIT 1`)
	paused := revision(p)
	if paused == ordered || paused == "" {
		t.Fatalf("revision did not follow the pause: %q → %q", ordered, paused)
	}
	// Retained ladder rows are history: Models inputs alone define routing.
	change(p, `DELETE FROM model_role_routes WHERE role='scout' AND priority=1`)
	if unchanged := revision(p); unchanged != paused {
		t.Fatalf("retired ladder history changed the routing revision: %q → %q", paused, unchanged)
	}
}
