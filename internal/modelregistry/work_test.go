// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/capacity"
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

// Risk: migration discards a saved ladder when merely opening Models had
// created a revision-zero workspace profile. Both pre-cutover states must
// retain their first choice through the durable Models migration.
func TestModelsV2UnadoptedSavedLadder(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(fmt.Sprint(initialized), func(t *testing.T) {
			prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
				ctx := t.Context()
				var opus string
				if err := tx.QueryRow(ctx, `SELECT id::text FROM model_profiles WHERE slug='claude-opus-xhigh'`).Scan(&opus); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,'build',1,$2)`, p.TenantID, opus); err != nil {
					return err
				}
				if initialized {
					if _, err := tx.Exec(ctx, `INSERT INTO model_pref_profiles(tenant_id,scope,revision) VALUES($1,'workspace',0)`, p.TenantID); err != nil {
						return err
					}
				}
				var runner, account string
				if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Saved ladder runner') RETURNING id::text`, p.TenantID).Scan(&runner); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner,allowed_model_profile_ids)
     VALUES($1,'saved-ladder','claude','saved-ladder-host',$2,'Saved ladder',now(),true,'generation',$3,ARRAY[$4::uuid]) RETURNING id::text`, p.TenantID, runner, p.ID, opus).Scan(&account); err != nil {
					return err
				}
				schedule := capacity.DefaultSchedule("UTC")
				schedule.Override = "sprint"
				schedule.Reserve = capacity.ReserveOff
				raw, _ := json.Marshal(schedule)
				if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, p.TenantID, p.ID, account, raw); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `SELECT aeon_migrate_models_v2($1)`, p.TenantID); err != nil {
					return err
				}
				now, err := dbNow(ctx, tx)
				if err != nil {
					return err
				}
				got, err := ResolveWork(ctx, tx, p, WorkQuery{Role: "build"}, now)
				if err != nil {
					return err
				}
				if got.Profile == nil || got.Profile.ID != opus || got.OwnerRequired || got.Trace.PreferenceOf == nil {
					t.Fatalf("initialized=%v discarded migrated build choice: %+v", initialized, got)
				}
				var rank []string
				if err := tx.QueryRow(ctx, `SELECT o.rank FROM model_pref_orders o JOIN model_pref_profiles profile ON profile.id=o.profile_id WHERE profile.scope='workspace' AND o.column_key='other' AND o.situation='first'`).Scan(&rank); err != nil {
					return err
				}
				if !reflect.DeepEqual(rank, []string{"anthropic:opus"}) {
					t.Fatalf("saved choice was not persisted: %v", rank)
				}
				return nil
			})
		})
	}
}

// enrollHarnesses gives each harness one healthy account with room for every
// profile, so the Models walk can run (it has no account-less preview).
func enrollHarnesses(t *testing.T, tx pgx.Tx, p tenant.Principal, harnesses ...string) {
	t.Helper()
	var runner string
	if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Work runner') RETURNING id::text`, p.TenantID).Scan(&runner); err != nil {
		t.Fatal(err)
	}
	schedule := capacity.DefaultSchedule()
	schedule.Override, schedule.Reserve = "sprint", capacity.ReserveOff
	raw, _ := json.Marshal(schedule)
	for _, h := range harnesses {
		var account string
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner)
 VALUES($1,$2,$2,'work-runner',$3,$2,now(),true,'work-generation',$4) RETURNING id::text`, p.TenantID, h, runner, p.ID).Scan(&account); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model)
 VALUES($1,$2,now()-interval '1 hour',now()+interval '1 day','requests',1000,'unrestricted')`, p.TenantID, account); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule)
 VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, p.TenantID, p.ID, account, raw); err != nil {
			t.Fatal(err)
		}
	}
}

// Risk: with nothing saved, a ticket's kind or size changes a role's route,
// or ticket routes and reviews disagree with dispatch. Models v2 sends every
// unsaved kind through the Default (no role ladder), so each kind and size
// must resolve exactly like the Default.
func TestResolveWorkEmptyMatrixEquivalence(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		enrollHarnesses(t, tx, p, "codex", "claude")
		now := time.Now()
		for _, role := range []string{"scout", "mechanical", "build", "build-hard"} {
			for _, h := range []string{"", "codex", "claude"} {
				defaultRoute, err := ResolveWork(t.Context(), tx, p, WorkQuery{Role: role, Harness: h}, now)
				if err != nil {
					return err
				}
				original := defaultRoute.Resolution
				if h == "" && original.Profile == nil {
					t.Fatalf("%s has no Default route: %+v", role, original)
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
							if ticket == nil || got.Profile == nil || ticket.Profile.ID != got.Profile.ID {
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
// Risk: a paused or retired model in a saved order still runs instead of the
// order's next step, or a residency requirement lets any role run on an
// account outside it. Pauses are Models v2's ladder holds.
func TestPreferenceFallbackAndResidencyBlocksEveryRole(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		enrollHarnesses(t, tx, p, "codex", "claude")
		profiles, err := listProfiles(t.Context(), tx)
		if err != nil {
			return err
		}
		pin, opus := profileBySlug(profiles, "codex-sol-xhigh"), profileBySlug(profiles, "claude-opus-xhigh")
		if err := dbtest.ModelsOrder(t.Context(), tx, p.TenantID, "backend", "first", "wait", pin.ID, opus.ID); err != nil {
			return err
		}
		pinLine, _ := modelsLine(pin)
		opusLine, _ := modelsLine(opus)
		held := func(got WorkResolution, reason string) bool {
			for _, h := range got.Trace.Held {
				if h.Line == pinLine && strings.Contains(h.Reason, reason) {
					return true
				}
			}
			return false
		}
		got, err := ResolveWork(t.Context(), tx, p, WorkQuery{Role: "build", Area: "backend"}, time.Now())
		if err != nil {
			return err
		}
		if got.Profile == nil {
			t.Fatalf("fixture must run the pinned line: %+v", got)
		}
		if line, _ := modelsLine(*got.Profile); line != pinLine {
			t.Fatalf("fixture must run the pinned line: %+v", got)
		}
		chosen := *got.Profile
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_pauses(tenant_id,harness,model,until_at,reason) VALUES($1,$2,$3,now()+interval '1 hour','test suppressed')`, p.TenantID, chosen.Harness, chosen.Model); err != nil {
			return err
		}
		got, err = ResolveWork(t.Context(), tx, p, WorkQuery{Role: "build", Area: "backend"}, time.Now())
		if err != nil {
			return err
		}
		if got.Profile == nil || got.Profile.Model == chosen.Model || !held(got, "test suppressed") {
			t.Fatalf("pause bypass: %+v", got)
		}
		if line, _ := modelsLine(*got.Profile); line != opusLine {
			t.Fatalf("pause did not fall to the order's next step: %+v", got)
		}
		if _, err := tx.Exec(t.Context(), `DELETE FROM model_pauses`); err != nil {
			return err
		}
		// Retiring every version of the line leaves it no route to run.
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profile_retirements(tenant_id,profile_id,reason,retired_by)
 SELECT tenant_id,id,'retired fixture',$2 FROM model_profiles WHERE aeon_models_line(family,harness,model)=$1`, pinLine, p.ID); err != nil {
			return err
		}
		got, err = ResolveWork(t.Context(), tx, p, WorkQuery{Role: "build", Area: "backend"}, time.Now())
		if err != nil {
			return err
		}
		if got.Profile == nil || !held(got, "no registered route for this line") {
			t.Fatalf("retired line ran or lost its cause: %+v", got)
		}
		if line, _ := modelsLine(*got.Profile); line != opusLine {
			t.Fatalf("retirement did not fall to the order's next step: %+v", got)
		}
		eu := "eu"
		if _, err := modelprefs.SaveScope(t.Context(), tx, p, modelprefs.Scope{Level: "default", Residency: &eu}); err != nil {
			return err
		}
		for _, role := range []string{"scout", "mechanical", "build", "build-hard"} {
			got, err := ResolveWork(t.Context(), tx, p, WorkQuery{Role: role, Area: "backend"}, time.Now())
			if err != nil {
				return err
			}
			if got.Profile != nil || !got.OwnerRequired || got.Trace.Blocked != "no eu model route" || got.CommandTemplate != "" || got.Residency != "eu" {
				t.Fatalf("%s escaped residency: %+v", role, got)
			}
			refused := false
			for _, c := range got.Ladder {
				if c.Selected {
					t.Fatal("blocked step selected")
				}
				refused = refused || slices.Contains(c.SkipReasons, "not allowed: eu")
			}
			if !refused {
				t.Fatalf("%s did not name residency on a refused route: %+v", role, got.Ladder)
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
		if n != 8 {
			t.Fatal("seed not idempotent", n)
		}
		return nil
	})
}

// Risk: queued rounds lose their previous-family fallback or saved review row
// while the Models migration is pending, misreport a plan-off cause, or bypass
// an explicit order or lock.
func TestModelsV2RoundRoutingCompatibility(t *testing.T) {
	admin, _ := boardFixture(t)
	boardDecode[boardDocument](t, boardCall(t, admin, "GET", "/api/model-preferences/board", nil, ""), 200)
	runner := addPrincipal(t, admin.TenantID, "agent", "Round runner", []string{"admin"})
	inRegistry(t, admin, func(tx pgx.Tx) error {
		ctx := tenant.WithPrincipal(t.Context(), admin)
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE model_pref_profiles SET thinking='deep' WHERE scope='workspace'`); err != nil {
			return err
		}
		for column, rank := range map[string][]string{
			"backend":       {"openai:sol", "anthropic:opus"},
			"design":        {"anthropic:opus", "openai:sol"},
			"review:openai": {"anthropic:opus", "openai:sol"},
		} {
			if _, err := tx.Exec(ctx, `INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank)
 SELECT $1,id,$2,'first',$3 FROM model_pref_profiles WHERE scope='workspace'`, admin.TenantID, column, rank); err != nil {
				return err
			}
		}
		schedule := capacity.DefaultSchedule("UTC")
		schedule.Override, schedule.Reserve = "sprint", capacity.ReserveOff
		raw, _ := json.Marshal(schedule)
		for _, harness := range []string{"codex", "claude"} {
			var account string
			if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner)
 VALUES($1,$2,$2,'round-fixture',$3,'Round fixture',$4,true,'round-generation',$5) RETURNING id::text`, admin.TenantID, harness, runner.ID, now, admin.ID).Scan(&account); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model)
 VALUES($1,$2,$3,$4,'requests',1000,'unrestricted')`, admin.TenantID, account, now.Add(-time.Hour), now.Add(time.Hour)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule)
 VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, admin.TenantID, admin.ID, account, raw); err != nil {
				return err
			}
		}
		var project, ticket string
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'ROUND-1','Round project' FROM node_kinds WHERE slug='project' RETURNING id::text`, admin.TenantID).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,fields) SELECT $1,id,'ROUND-2','Round work',$2,'{"area":"backend","estimate_hours":4}' FROM node_kinds WHERE slug='work' RETURNING id::text`, admin.TenantID, project).Scan(&ticket); err != nil {
			return err
		}
		for _, tc := range []struct{ kind, situation, column, family, effort string }{
			{"first_build", "first", "backend", "openai", "xhigh"},
			{"fix", "stuck", "backend", "anthropic", "xhigh"},
			{"review", "first", "review:openai", "anthropic", "xhigh"},
		} {
			t.Run(tc.kind, func(t *testing.T) {
				q := WorkQuery{TicketID: ticket, ProjectID: project, FixRound: 4, PreviousFamily: "openai", AuthorFamily: "openai"}
				got, design, err := ResolveRound(ctx, tx, admin, q, tc.kind, true, now)
				if err != nil {
					t.Fatal(err)
				}
				if design || got.Profile == nil || got.Profile.Family != tc.family || got.Profile.Effort != tc.effort || got.Trace.Column != tc.column || got.Trace.Situation != tc.situation || got.OwnerRequired || len(got.Trace.QualifyingAccountIDs) == 0 {
					t.Fatalf("round lost compatibility: %+v; profile %+v", got, got.Profile)
				}
			})
		}
		t.Run("plan off cause", func(t *testing.T) {
			q := WorkQuery{TicketID: ticket, ProjectID: project, OffHarnesses: []string{"claude"}}
			got, _, err := ResolveRound(ctx, tx, admin, q, "design", true, now)
			if err != nil {
				t.Fatal(err)
			}
			if got.Profile == nil || got.Profile.Harness != "codex" || len(got.Trace.Held) == 0 || got.Trace.Held[0].Line != "anthropic:opus" || !strings.Contains(got.Trace.Held[0].Reason, "off in the plan") {
				t.Fatalf("plan-off fallback lost its cause: %+v", got)
			}
		})
		q := WorkQuery{TicketID: ticket, ProjectID: project, FixRound: 4}
		checkStuck := func() {
			t.Helper()
			got, _, err := ResolveRound(ctx, tx, admin, q, "fix", true, now)
			if err != nil {
				t.Fatal(err)
			}
			if got.Profile == nil || got.Profile.Family != "openai" || got.Trace.Situation != "stuck" {
				t.Fatalf("explicit order or lock was reordered: %+v; profile %+v", got, got.Profile)
			}
		}
		// Ordinary Models routing keeps the row when no previous family is sent.
		checkStuck()
		q.PreviousFamily = "openai"
		if _, err := tx.Exec(ctx, `INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank)
 SELECT $1,id,'backend','stuck',ARRAY['openai:sol','anthropic:opus'] FROM model_pref_profiles WHERE scope='workspace'`, admin.TenantID); err != nil {
			return err
		}
		checkStuck()
		if _, err := tx.Exec(ctx, `DELETE FROM model_pref_orders WHERE column_key='backend' AND situation='stuck'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_rules(tenant_id,scope,column_key,line,lock,why)
 VALUES($1,'workspace','backend','openai:sol','top','Freeze the workspace order')`, admin.TenantID); err != nil {
			return err
		}
		checkStuck()
		// An exhausted saved review chain must Wait instead of silently running
		// Automatic or the build Default; its author's family never qualifies.
		if _, err := tx.Exec(ctx, `UPDATE model_pref_orders SET rank=ARRAY['openai:sol'] WHERE column_key='review:openai'`); err != nil {
			return err
		}
		q.AuthorFamily = "openai"
		waited, _, err := ResolveRound(ctx, tx, admin, q, "review", true, now)
		if err != nil {
			return err
		}
		if waited.Profile != nil || !waited.OwnerRequired || waited.Trace.Blocked == "" || len(waited.Trace.QualifyingAccountIDs) != 0 || waited.Trace.OrderMode != reviewOrderSaved || waited.Trace.Column != "review:openai" {
			t.Fatalf("saved review chain escaped Wait or family policy: %+v", waited)
		}
		return nil
	})
}

// Risk: the resolver ignores Models, tries another model before its sibling
// route, admits an author's family/security Anthropic, or blocks unknown quota.
func TestModelsV2RolesRoutesAndWait(t *testing.T) {
	admin, _ := boardFixture(t)
	runner := addPrincipal(t, admin.TenantID, "agent", "Models v2 runner", []string{"admin"})
	var cursor Profile
	var now, until time.Time
	accounts := map[string]string{}
	var ticket string
	inRegistry(t, admin, func(tx pgx.Tx) error {
		var err error
		cursor, err = insertProfile(t.Context(), tx, admin.TenantID, profileWrite{Slug: "cursor-opus-v2", Version: "1", Harness: "cursor", Family: "anthropic", Model: "claude-opus-5-5", Effort: "xhigh", Tier: "strong"})
		return err
	})
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/other/first?for=default", map[string]any{"rank": []string{"anthropic:opus", "openai:sol"}, "not": []string{}, "revision": 0}, ""), 200)
	boardDecode[boardWriteResult](t, boardCall(t, admin, "PUT", "/api/model-preferences/orders/other/first/thinking?for=default", map[string]any{"effort": "xhigh", "revision": 1}, ""), 200)
	inRegistry(t, admin, func(tx pgx.Tx) error {
		ctx := tenant.WithPrincipal(t.Context(), admin)
		var err error
		now, err = dbNow(ctx, tx)
		if err != nil {
			return err
		}
		until = now.Add(time.Hour)
		start, _, err := agentplan.LocalDay(now, "UTC")
		if err != nil {
			return err
		}
		schedule := capacity.DefaultSchedule("UTC")
		schedule.Override = "sprint"
		schedule.OverrideUntil = &until
		schedule.Reserve = capacity.ReserveOff
		raw, _ := json.Marshal(schedule)
		for _, h := range []string{"claude", "cursor", "codex"} {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,state,last_probe_at,last_probe_ok,last_daemon_generation,owner_person_id,linked_at)
    VALUES($1,$2,$2,'models-v2',$3,$2,'available',$4,true,'v2-generation',$5,$6) RETURNING id::text`, admin.TenantID, h, runner.ID, now, admin.ID, start.Add(-time.Hour)).Scan(&id); err != nil {
				return err
			}
			accounts[h] = id
			if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, admin.TenantID, admin.ID, id, raw); err != nil {
				return err
			}
		}
		for _, tc := range []struct{ role, author, family string }{
			{"build", "", "anthropic"}, {"build-hard", "", "anthropic"}, {"scout", "", "openai"}, {"mechanical", "", "openai"}, {"review-gate", "openai", "anthropic"}, {"review-gate", "anthropic", ""}, {"review-gate-security", "openai", ""}, {"review-gate-security", "anthropic", ""},
		} {
			got, err := ResolveWork(ctx, tx, admin, WorkQuery{Role: tc.role, AuthorFamily: tc.author}, now)
			if err != nil {
				return err
			}
			if tc.family == "" {
				if got.Profile != nil || !got.OwnerRequired || len(got.Trace.QualifyingAccountIDs) != 0 {
					t.Fatalf("unqualified reviewer ran: %+v", got)
				}
				for _, candidate := range got.Ladder {
					if candidate.Selected {
						t.Fatalf("Wait selected reviewer: %+v", candidate)
					}
				}
				continue
			}
			if got.Profile == nil || got.Profile.Family != tc.family || got.OwnerRequired || len(got.Trace.QualifyingAccountIDs) == 0 {
				t.Fatalf("%s unknown quota: %+v", tc.role, got)
			}
			if tc.role == "scout" || tc.role == "mechanical" {
				if got.Profile.Model != "gpt-6-luna" || got.Ladder[len(got.Ladder)-1].Stage != "column" || got.Trace.Prefs != "quick jobs" {
					t.Fatalf("cheapest fast: %+v", got)
				}
			}
			if strings.HasPrefix(tc.role, "review-gate") && (got.Profile.Family == tc.author || !qualifiedReviewProfile(*got.Profile)) {
				t.Fatalf("review rule escaped: %+v", got)
			}
		}
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1,id,'MV2-1','Models work','{"route_role":"build","area":"backend","estimate_hours":2,"fix_round":1}' FROM node_kinds WHERE slug='work' RETURNING id::text`, admin.TenantID).Scan(&ticket)
	})
	// CLI without and with a ticket shares Models and canonical caller.
	resolved := decode[WorkResolution](t, &admin, "GET", "/api/models/resolve?role=build", "", 200)
	if resolved.Profile == nil || resolved.Profile.Model != "opus" || resolved.Trace.Situation != "first" || resolved.Trace.PersonID == nil || *resolved.Trace.PersonID != admin.ID {
		t.Fatalf("CLI ignored Default/caller: %+v", resolved)
	}
	ticketed := decode[WorkResolution](t, &admin, "GET", "/api/models/resolve?role=build&ticket="+ticket, "", 200)
	if ticketed.Profile == nil || ticketed.Profile.Model != "opus" || ticketed.Trace.Column != "backend" || ticketed.Trace.Situation != "fix" {
		t.Fatalf("CLI ignored ticket placement: %+v", ticketed)
	}
	inRegistry(t, admin, func(tx pgx.Tx) error {
		ctx := tenant.WithPrincipal(t.Context(), admin)
		// A fresh daily cap skips only Claude's route, before the next model.
		start, end, err := agentplan.LocalDay(now, "UTC")
		if err != nil {
			return err
		}
		until = end
		plan, _ := json.Marshal(agentplan.Plan{Total: 5, Limits: map[string]agentplan.Limit{}, Daily: map[string]agentplan.DailySettings{"claude": {Pace: agentplan.DailyPace{Mode: "pace"}, AtLimit: "wait"}}})
		if _, err := tx.Exec(ctx, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agents.working',$3) ON CONFLICT(tenant_id,principal_id,key) DO UPDATE SET value=EXCLUDED.value`, admin.TenantID, admin.ID, plan); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model,capacity_read_at,capacity_allowed,capacity_kind,capacity_bucket,capacity_source) VALUES($1,$2,$3,$4,'percent',100,50,'unrestricted',$5,true,'weekly','daily','harness')`, admin.TenantID, accounts["claude"], start, end.Add(5*24*time.Hour), now); err != nil {
			return err
		}
		for _, reading := range []struct {
			at   time.Time
			used float64
		}{{start.Add(time.Minute), 40}, {now, 50}} {
			if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source) VALUES($1,$2,'weekly','daily',10080,$3,$4,$5,'harness')`, admin.TenantID, accounts["claude"], reading.used, end.Add(5*24*time.Hour), reading.at); err != nil {
				return err
			}
		}

		snapshot, err := agentaccounts.ReadDailyPolicyTx(ctx, tx, admin.TenantID, admin.ID, now)
		if err != nil {
			return err
		}
		capped := false
		for _, account := range snapshot.DailyState["claude"].Accounts {
			if account.AccountID == accounts["claude"] && agentplan.DailyAccountReason(account, now) == "daily_limit" {
				capped = true
			}
		}
		if !capped {
			t.Fatalf("fixture did not establish the daily cap: %+v", snapshot.DailyState["claude"])
		}

		got, err := ResolveWork(ctx, tx, admin, WorkQuery{Role: "build"}, now)
		if err != nil {
			return err
		}
		if got.Profile == nil || got.Profile.ID != cursor.ID || len(got.Trace.Held) != 0 || len(got.Ladder) < 2 {
			t.Fatalf("sibling route was skipped: %+v", got)
		}
		if got.Ladder[0].Selected || !strings.Contains(got.Ladder[0].SkipReasons[0], "daily cap until "+until.UTC().Format(time.RFC3339)) {
			t.Fatalf("missing cap/reset cause: %+v", got.Ladder)
		}
		if _, err := tx.Exec(ctx, `UPDATE model_pref_orders SET rank=ARRAY['anthropic:opus','missing:route'] WHERE column_key='other' AND situation='first'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET state='unavailable' WHERE harness='cursor'`); err != nil {
			return err
		}
		waited, err := ResolveWork(ctx, tx, admin, WorkQuery{Role: "build"}, now)
		if err != nil {
			return err
		}
		if waited.Profile != nil || !waited.OwnerRequired || len(waited.Trace.Held) != 2 || waited.Trace.Held[1].Line != "missing:route" || waited.Trace.Held[1].Reason != "no registered route for this line" || waited.Trace.Blocked != waited.Ladder[0].SkipReasons[0] || !strings.Contains(waited.Trace.Blocked, until.UTC().Format(time.RFC3339)) {
			t.Fatalf("Wait lost first cause or ran hidden fallback: %+v", waited)
		}
		for _, step := range waited.Ladder {
			if step.Stage != "column" {
				t.Fatalf("hidden tail ran: %+v", step)
			}
		}
		return nil
	})
}

// Risk: selecting a level globally drops a sibling route's native level or
// reviews run below the highest available native level.
func TestModelsV2NativeLevelsPerRoute(t *testing.T) {
	ptr := func(v int) *int { return &v }
	profiles := []Profile{
		{ID: "claude", Harness: "claude", Family: "anthropic", Model: "opus", Effort: "xhigh", EffortLevel: ptr(4), Tier: "strong", Enabled: true},
		{ID: "cursor-high", Harness: "cursor", Family: "anthropic", Model: "claude-opus-5-5", Effort: "high", EffortLevel: ptr(3), Tier: "strong", Enabled: true},
		{ID: "cursor-ultra", Harness: "cursor", Family: "anthropic", Model: "claude-opus-5-5", Effort: "ultra", EffortLevel: ptr(6), Tier: "strong", Enabled: true},
	}
	got := modelsRouteCandidates(profiles, "xhigh", 4, false)
	if len(got) != 2 || got[0].ID != "claude" || got[1].ID != "cursor-high" {
		t.Fatalf("nearest per route: %+v", got)
	}
	review := modelsRouteCandidates(profiles, "xhigh", 4, true)
	if len(review) != 2 || review[1].ID != "cursor-ultra" || !qualifiedReviewProfile(review[1]) {
		t.Fatalf("highest native review: %+v", review)
	}
	state := modelprefs.BoardState{Orders: []modelprefs.BoardOrder{{Rank: []string{"anthropic:claude-opus"}, Not: []string{"anthropic:claude-opus"}, Efforts: map[string]string{"anthropic:claude-opus": "xhigh"}}}, Rules: []modelprefs.Rule{{Line: "anthropic:claude-opus"}}}
	normalized := modelsBoardIdentities(state, boardCatalog{profiles: map[string][]Profile{"anthropic:claude-opus": profiles[1:]}})
	if normalized.Orders[0].Rank[0] != "anthropic:opus" || normalized.Orders[0].Not[0] != "anthropic:opus" || normalized.Orders[0].Efforts["anthropic:opus"] != "xhigh" || normalized.Rules[0].Line != "anthropic:opus" || state.Orders[0].Rank[0] != "anthropic:claude-opus" || state.Rules[0].Line != "anthropic:claude-opus" {
		t.Fatalf("stored pick identity or immutability lost: %+v / %+v", normalized, state)
	}
	ties := profiles[1:]
	ties[0].EffortLevel = ptr(2)
	ties[1].EffortLevel = ptr(4)
	if tie := modelsRouteCandidates(ties, "", 3, false); len(tie) != 1 || tie[0].ID != "cursor-ultra" {
		t.Fatalf("native tie did not go up: %+v", tie)
	}
}
