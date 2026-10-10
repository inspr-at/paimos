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
		if n != 8 {
			t.Fatal("seed not idempotent", n)
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
				if got.Profile.Model != "gpt-6-luna" || got.Ladder[len(got.Ladder)-1].Stage != "quick" {
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
		for _, reading := range []struct {
			at   time.Time
			used float64
		}{{start.Add(time.Minute), 40}, {now, 50}} {
			if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source) VALUES($1,$2,'weekly','',10080,$3,$4,$5,'harness')`, admin.TenantID, accounts["claude"], reading.used, end.Add(5*24*time.Hour), reading.at); err != nil {
				return err
			}
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
		if _, err := tx.Exec(ctx, `UPDATE model_pref_orders SET rank=ARRAY['anthropic:opus'] WHERE column_key='other' AND situation='first'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET state='paused' WHERE harness='cursor'`); err != nil {
			return err
		}
		waited, err := ResolveWork(ctx, tx, admin, WorkQuery{Role: "build"}, now)
		if err != nil {
			return err
		}
		if waited.Profile != nil || !waited.OwnerRequired || len(waited.Trace.Held) != 1 || waited.Trace.Blocked != waited.Ladder[0].SkipReasons[0] || !strings.Contains(waited.Trace.Blocked, until.UTC().Format(time.RFC3339)) {
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
