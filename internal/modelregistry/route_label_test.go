// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
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
	inRegistry(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM model_role_routes WHERE role='scout' AND priority=1`)
		return err
	})
	if unchanged := revision(p); unchanged != seeded {
		t.Fatalf("retired ladder history changed the routing revision: %q → %q", seeded, unchanged)
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
	if paused := revision(p); paused == ordered || paused == "" {
		t.Fatalf("revision did not follow the pause: %q → %q", ordered, paused)
	}
}

// Risk: writes bound orders per workspace-and-person board and row constraints
// per workspace-and-project, while the registry digest bounded their bodies
// tenant-wide. A team whose every board was accepted then broke planning's
// routing revision and the line editor's token. Both digests must still move
// when a real order or constraint write lands.
func TestRegistryRevisionAcceptsOrdersAndConstraintsAcrossOwners(t *testing.T) {
	admin, member := boardFixture(t)
	inRegistry(t, admin, func(tx pgx.Tx) error { return ensureCatalog(t.Context(), tx, admin) })
	ctx := dbtest.Seed(t.Context())
	var orders, constraints int
	if err := db.InTenant(ctx, appPool, admin.TenantID, func(tx pgx.Tx) error {
		var kinds, lines int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM work_kinds WHERE archived_at IS NULL AND project_id IS NULL),
 (SELECT count(DISTINCT aeon_models_line(family,harness,model)) FROM model_profiles WHERE enabled)`).Scan(&kinds, &lines); err != nil {
			return err
		}
		if kinds < 2 || lines < 2 {
			return fmt.Errorf("fixture needs kinds and lines: %d %d", kinds, lines)
		}
		// Every person keeps an order for each kind and situation, far below
		// the 1536 orders one board may hold; together they exceed it.
		people := 1536/(kinds*5) + 2
		if _, err := tx.Exec(ctx, `WITH people AS (
 INSERT INTO principals(tenant_id,kind,name,roles) SELECT $1,'person','Planner '||n,'{member}' FROM generate_series(1,$2) n RETURNING id
), profiles AS (
 INSERT INTO model_pref_profiles(tenant_id,scope,person_id,revision) SELECT $1,'person',id,1 FROM people RETURNING id
), kinds AS (SELECT slug FROM work_kinds WHERE archived_at IS NULL AND project_id IS NULL), base AS (
 INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank,not_allowed)
 SELECT $1,p.id,k.slug,s,'{openai:sol}','{}' FROM profiles p CROSS JOIN kinds k CROSS JOIN unnest(ARRAY['first','fix','stuck']) s RETURNING 1
)
INSERT INTO model_pref_situation_orders(tenant_id,profile_id,column_key,situation,rank,not_allowed)
 SELECT $1,p.id,k.slug,s,'{openai:sol}','{}' FROM profiles p CROSS JOIN kinds k CROSS JOIN unnest(ARRAY['small','review']) s`, admin.TenantID, people); err != nil {
			return err
		}
		// Each project row keeps at most kinds × lines constraints, far below
		// the 4096 rules a workspace-and-project write may hold.
		projects := 4096/(kinds*lines) + 2
		if _, err := tx.Exec(ctx, `WITH projects AS (
 INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,(SELECT id FROM node_kinds WHERE slug='project'),'RC-'||n,'Project '||n FROM generate_series(1,$2) n RETURNING id
), revisions AS (
 INSERT INTO model_rule_revisions(tenant_id,scope,scope_key,revision) SELECT $1,'project',id::text,1 FROM projects RETURNING 1
)
INSERT INTO model_row_constraints(tenant_id,scope,project_id,column_key,line,kind,why)
 SELECT $1,'project',p.id,k.slug,l.line,'never','Project exclusion' FROM projects p
 CROSS JOIN (SELECT slug FROM work_kinds WHERE archived_at IS NULL AND project_id IS NULL) k
 CROSS JOIN (SELECT DISTINCT aeon_models_line(family,harness,model) AS line FROM model_profiles WHERE enabled) l`, admin.TenantID, projects); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM (`+modelprefs.OrdersSQL+`) o),(SELECT count(*) FROM model_row_constraints)`).Scan(&orders, &constraints)
	}); err != nil {
		t.Fatal(err)
	}
	if orders <= 1536 || constraints <= 4096 {
		t.Fatalf("fixture stays within the tenant-wide bounds: %d orders, %d constraints", orders, constraints)
	}
	digests := func() (string, string) {
		t.Helper()
		var routing, editor string
		inRegistry(t, admin, func(tx pgx.Tx) error {
			var err error
			if routing, err = Revision(t.Context(), tx); err != nil {
				return err
			}
			editor, err = registryRevision(t.Context(), tx)
			return err
		})
		if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(routing) || len(editor) != 64 {
			t.Fatalf("digests %q %q", routing, editor)
		}
		return routing, editor
	}
	routing, editor := digests()
	// One person's real order write still moves both digests.
	boardDecode[boardWriteResult](t, boardCall(t, member, "PUT", "/api/model-preferences/orders/backend/first?for=me", map[string]any{"rank": []string{"anthropic:opus", "openai:sol"}, "not": []string{}, "revision": 0}, member.ID), 200)
	ordered, orderedEditor := digests()
	if ordered == routing || orderedEditor == editor {
		t.Fatalf("an order write left a digest unchanged: %q → %q, %q → %q", routing, ordered, editor, orderedEditor)
	}
	// So does a real row-constraint write.
	boardDecode[rulesDocument](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/backend/first/never/openai:astra", map[string]any{"why": "Backend exclusion", "revision": 0}, ""), 200)
	if constrained, constrainedEditor := digests(); constrained == ordered || constrainedEditor == orderedEditor {
		t.Fatalf("a constraint write left a digest unchanged: %q → %q, %q → %q", ordered, constrained, orderedEditor, constrainedEditor)
	}
}
