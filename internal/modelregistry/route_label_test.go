// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
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

// The revision is empty before the registry is seeded and changes whenever a
// route changes, so a list can name what it resolved against.
func TestRevisionFollowsRoutes(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "route-revision", "person", "lead", []string{"admin"})
	revision := func() string {
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
	if got := revision(); got != "" {
		t.Fatalf("unseeded revision %q", got)
	}
	if code, body := call(t, &p, http.MethodGet, "/api/models", ""); code != http.StatusOK {
		t.Fatalf("seed: %d %s", code, body)
	}
	seeded := revision()
	if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(seeded) || revision() != seeded {
		t.Fatalf("seeded revision %q", seeded)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM model_role_routes WHERE role='scout' AND priority=1`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed := revision(); changed == seeded || changed == "" {
		t.Fatalf("revision did not follow the routes: %q → %q", seeded, changed)
	}
}
