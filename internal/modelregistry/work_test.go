// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func prefsFixture(t *testing.T, fn func(pgx.Tx, tenant.Principal) error) {
	t.Helper()
	reset(t)
	p := makePrincipal(t, "prefs", "person", "Starter", []string{"admin"})
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if err := ensureCatalog(t.Context(), tx, p); err != nil {
			return err
		}
		if err := modelprefs.SeedKinds(t.Context(), tx, p.TenantID); err != nil {
			return err
		}
		return fn(tx, p)
	}); err != nil {
		t.Fatal(err)
	}
}
func TestResolveWorkEmptyMatrixEquivalence(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		now := time.Now()
		for _, role := range []string{"scout", "mechanical", "build", "build-hard"} {
			for _, h := range []string{"", "codex", "claude"} {
				original, err := resolveRole(t.Context(), tx, resolveQuery{Role: role, Harness: h}, now)
				if err != nil {
					return err
				}
				for _, c := range []string{"", "S", "M", "L"} {
					for _, area := range []string{"backend", "frontend", "full-stack", "infra", "design", "docs", "security", "firmware", "", "review", "other"} {
						got, err := ResolveWork(t.Context(), tx, p, WorkQuery{Role: role, Area: area, Complexity: c, Harness: h}, now)
						if err != nil {
							return err
						}
						a, _ := json.Marshal(original)
						b, _ := json.Marshal(got.Resolution)
						if string(a) != string(b) {
							t.Fatalf("%s/%s/%s/%s changed ladder: %s != %s", role, h, c, area, a, b)
						}
						if known, err := KnownRouteArea(t.Context(), tx, area, ""); err == nil && known && h == "" {
							ticket, err := ResolveTicketRoute(t.Context(), tx, role, area, now)
							if err != nil {
								return err
							}
							if ticket == nil || ticket.Profile.ID != got.Profile.ID {
								t.Fatal("known area route changed")
							}
						}
					}
				}
			}
		}
		for _, role := range []string{"review-gate", "review-gate-security", "", "bogus"} {
			if _, err := ResolveWork(t.Context(), tx, p, WorkQuery{Role: role, Area: "backend"}, now); err == nil {
				t.Fatalf("missing error for %s", role)
			}
		}
		for _, author := range []string{"openai", "anthropic", "xai", "cursor"} {
			original, err := ResolveReview(t.Context(), tx, p, author, "", now)
			if err != nil {
				return err
			}
			for _, c := range []string{"", "S", "M", "L"} {
				for _, role := range []string{"build", "build-hard"} {
					got, err := ResolveReviewFor(t.Context(), tx, p, WorkQuery{AuthorFamily: author, Complexity: c, TicketRole: role}, now)
					if err != nil {
						return err
					}
					if !reflect.DeepEqual(original.Ladder, got.Ladder) || !reflect.DeepEqual(original.Profile, got.Profile) {
						t.Fatal("review changed with empty matrix")
					}
				}
			}
		}
		return nil
	})
}
func TestPreferenceFallbackAndResidencyBlocksEveryRole(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		profiles, err := listProfiles(t.Context(), tx)
		if err != nil {
			return err
		}
		var pin Profile
		for _, v := range profiles {
			if v.Slug == "codex-sol-xhigh" {
				pin = v
			}
		}
		scope, err := modelprefs.SaveScope(t.Context(), tx, p, modelprefs.Scope{Level: "default"})
		if err != nil {
			return err
		}
		kind, _, err := modelprefs.LookupKind(t.Context(), tx, "backend", "")
		if err != nil {
			return err
		}
		if err := modelprefs.PutRow(t.Context(), tx, p, scope, kind.ID, modelprefs.Row{Cells: map[string]modelprefs.Cell{"normal": {Mode: "pinned", ProfileID: pin.ID}}}); err != nil {
			return err
		}
		// Suppression is on the run role even when its profile is off that ladder.
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id,state,reason,valid_until)
   VALUES($1,'build',99,$2,'unavailable','test suppressed',now()+interval '1 hour')`, p.TenantID, pin.ID); err != nil {
			return err
		}
		got, err := ResolveWork(t.Context(), tx, p, WorkQuery{Role: "build", Area: "backend"}, time.Now())
		if err != nil {
			return err
		}
		if got.Profile == nil || got.Profile.ID == pin.ID || !strings.Contains(got.Trace.Fallback, "suppressed") {
			t.Fatalf("suppression bypass: %+v", got)
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profile_retirements(tenant_id,profile_id,reason,retired_by) VALUES($1,$2,'retired fixture',$3)`, p.TenantID, pin.ID, p.ID); err != nil {
			return err
		}
		got, err = ResolveWork(t.Context(), tx, p, WorkQuery{Role: "build", Area: "backend"}, time.Now())
		if err != nil {
			return err
		}
		if !strings.Contains(got.Trace.Fallback, "retired") {
			t.Fatal(got.Trace)
		}
		eu := "eu"
		scope.Residency = &eu
		scope, err = modelprefs.SaveScope(t.Context(), tx, p, scope)
		if err != nil {
			return err
		}
		for _, role := range []string{"scout", "mechanical", "build", "build-hard"} {
			got, err := ResolveWork(t.Context(), tx, p, WorkQuery{Role: role, Area: "backend"}, time.Now())
			if err != nil {
				return err
			}
			if got.Profile != nil || !got.OwnerRequired || got.Trace.Blocked != "no eu model route" || got.CommandTemplate != "" {
				t.Fatalf("%s escaped residency: %+v", role, got)
			}
			for _, c := range got.Ladder {
				if c.Selected {
					t.Fatal("blocked step selected")
				}
			}
		}
		for _, q := range []WorkQuery{{AuthorFamily: "openai", Area: "security"}, {AuthorFamily: "openai", Role: "review-gate-security"}} {
			r, err := ResolveReviewFor(t.Context(), tx, p, q, time.Now())
			if err != nil {
				return err
			}
			if r.Profile != nil || !r.OwnerRequired || r.Role != "review-gate-security" {
				t.Fatal("security fell back", r)
			}
		}
		return nil
	})
}

func TestResolveWorkReviewInfersCanonicalStarter(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		var alias, agent string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,linked_to)
   VALUES($1,'person','Linked reviewer',$2) RETURNING id::text`, p.TenantID, p.ID).Scan(&alias); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name)
   VALUES($1,'agent','Review starter') RETURNING id::text`, p.TenantID).Scan(&agent); err != nil {
			return err
		}
		eu := "eu"
		if _, err := modelprefs.SaveScope(t.Context(), tx, p, modelprefs.Scope{Level: "person", PersonID: &p.ID, Residency: &eu}); err != nil {
			return err
		}
		for _, tc := range []struct {
			name       string
			who        tenant.Principal
			wantPerson bool
		}{
			{"person", p, true},
			{"linked person", tenant.Principal{ID: alias, TenantID: p.TenantID, Kind: tenant.Person}, true},
			{"creator key", tenant.Principal{ID: agent, TenantID: p.TenantID, Kind: tenant.Agent, KeyCreatorID: alias}, true},
			{"operator key", tenant.Principal{ID: agent, TenantID: p.TenantID, Kind: tenant.Agent}, false},
		} {
			got, err := ResolveWork(t.Context(), tx, tc.who, WorkQuery{Role: "review-gate", AuthorFamily: "openai"}, time.Now())
			if err != nil {
				return err
			}
			if tc.wantPerson {
				if got.Trace.PersonID == nil || *got.Trace.PersonID != p.ID || got.Residency != "eu" {
					t.Fatal(tc.name, "lost canonical starter", got)
				}
			} else if got.Trace.PersonID != nil || got.Residency != "any" {
				t.Fatal("operator key gained You scope", got)
			}
		}
		return nil
	})
}

func expectConstraint(t *testing.T, tx pgx.Tx, code, sql string, args ...any) {
	t.Helper()
	nested, err := tx.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = nested.Exec(t.Context(), sql, args...)
	_ = nested.Rollback(t.Context())
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != code {
		t.Fatalf("wanted %s, got %v", code, err)
	}
}
func TestPreferenceStoreCanonicalPeopleAndGuards(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		var alias, agent, inactive, project, ticket string
		for i, dst := range []*string{&alias, &agent, &inactive} {
			kind := "person"
			if i == 1 {
				kind = "agent"
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,$3) RETURNING id::text`, p.TenantID, kind, fmt.Sprintf("Fixture %d", i)).Scan(dst); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(t.Context(), `UPDATE principals SET linked_to=$2 WHERE id=$1`, alias, p.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE principals SET status='deactivated' WHERE id=$1`, inactive); err != nil {
			return err
		}
		for _, tt := range []struct{ id, want string }{{p.ID, p.ID}, {alias, p.ID}, {agent, ""}, {inactive, ""}, {"00000000-0000-0000-0000-000000000001", ""}} {
			got, err := modelprefs.CanonicalPerson(t.Context(), tx, tt.id)
			if err != nil {
				return err
			}
			var fromSQL *string
			if err := tx.QueryRow(t.Context(), `SELECT `+modelprefs.CanonicalPersonSQL("$1")+`::text`, tt.id).Scan(&fromSQL); err != nil {
				return err
			}
			if !reflect.DeepEqual(got, fromSQL) || (tt.want == "" && got != nil) || (tt.want != "" && (got == nil || *got != tt.want)) {
				t.Fatal(tt, got, fromSQL)
			}
		}
		for _, who := range []tenant.Principal{{ID: alias, Kind: tenant.Person}, {ID: agent, Kind: tenant.Agent, KeyCreatorID: alias}, {ID: agent, Kind: tenant.Agent}, {ID: agent, Kind: tenant.Agent, KeyCreatorID: inactive}} {
			got := modelprefs.PrefsPerson(t.Context(), tx, who)
			if who.Kind == tenant.Person || who.KeyCreatorID == alias {
				if got == nil || *got != p.ID {
					t.Fatal("starter mapping", who, got)
				}
			} else if got != nil {
				t.Fatal("unexpected You", who, got)
			}
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'PREFS-1','Project' FROM node_kinds WHERE slug='project' RETURNING id::text`, p.TenantID).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,'PREFS-2','Ticket',$2 FROM node_kinds WHERE slug='work' RETURNING id::text`, p.TenantID, project).Scan(&ticket); err != nil {
			return err
		}
		expectConstraint(t, tx, "23514", `INSERT INTO model_pref_scopes(tenant_id,level,person_id) VALUES($1,'person',$2)`, p.TenantID, alias)
		expectConstraint(t, tx, "23514", `INSERT INTO model_pref_scopes(tenant_id,level,project_id) VALUES($1,'project',$2)`, p.TenantID, ticket)
		expectConstraint(t, tx, "23514", `INSERT INTO model_pref_scopes(tenant_id,level,project_id,prefs_locked) VALUES($1,'project',$2,true)`, p.TenantID, project)
		expectConstraint(t, tx, "23505", `INSERT INTO work_kinds(tenant_id,slug,label,project_id) VALUES($1,'backend','Backend again',$2)`, p.TenantID, project)
		expectConstraint(t, tx, "23514", `UPDATE work_kinds SET label='Renamed' WHERE slug='security'`)
		expectConstraint(t, tx, "23514", `DELETE FROM work_kinds WHERE slug='review'`)
		expectConstraint(t, tx, "23514", `INSERT INTO work_kinds(tenant_id,slug,label,project_id) VALUES($1,'security','Project security',$2)`, p.TenantID, project)
		scope, err := modelprefs.SaveScope(t.Context(), tx, p, modelprefs.Scope{Level: "person", PersonID: &p.ID})
		if err != nil {
			return err
		}
		kind, _, err := modelprefs.LookupKind(t.Context(), tx, "backend", project)
		if err != nil {
			return err
		}
		if err := modelprefs.PutRow(t.Context(), tx, p, scope, kind.ID, modelprefs.Row{Cells: map[string]modelprefs.Cell{"normal": {Mode: "auto"}}}); err != nil {
			return err
		}
		chain, err := modelprefs.LoadChain(t.Context(), tx, &alias, project)
		if err != nil {
			return err
		}
		if r := modelprefs.ResolveCell(chain, "backend", "normal"); r.Cell == nil || r.SetBy != "person" {
			t.Fatal("alias missed canonical prefs", r)
		}
		for _, area := range []string{"review", "other", "", "unknown"} {
			k, _, err := modelprefs.LookupKind(t.Context(), tx, area, project)
			if err != nil {
				return err
			}
			if k.Slug != "other" {
				t.Fatal("build reads review row", k)
			}
		}
		var schema []byte
		if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET label=label WHERE slug='work'`); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT field_schema->'properties'->'area' FROM node_kinds WHERE slug='work'`).Scan(&schema); err != nil {
			return err
		}
		if strings.Contains(string(schema), "enum") || !strings.Contains(string(schema), "pattern") {
			t.Fatal("classification trigger restored area enum", string(schema))
		}
		if err := modelprefs.SeedKinds(t.Context(), tx, p.TenantID); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM work_kinds`).Scan(&n); err != nil {
			return err
		}
		if n != 9 {
			t.Fatal("seed not idempotent", n)
		}
		return nil
	})
}
