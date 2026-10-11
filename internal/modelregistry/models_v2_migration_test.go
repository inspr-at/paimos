// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func modelsMigrationFixture(t *testing.T) (tenant.Principal, tenant.Principal) {
	t.Helper()
	admin, member := boardFixture(t)
	boardDecode[boardDocument](t, boardCall(t, admin, "GET", "/api/model-preferences/board", nil, ""), 200)
	return admin, member
}

// Risk: migration changes a saved fallback/Wait, drops a hold or a rule's
// ownership, or emits an incomplete/duplicate migration audit.
func TestModelsV2MigrationGolden(t *testing.T) {
	admin, waiter := modelsMigrationFixture(t)
	runner := addPrincipal(t, admin.TenantID, "agent", "Migration runner", nil)
	var now, until time.Time
	var workspace string
	ids := map[string]string{}
	inRegistry(t, admin, func(tx pgx.Tx) error {
		ctx := t.Context()
		var err error
		now, err = dbNow(ctx, tx)
		if err != nil {
			return err
		}
		until = now.Add(time.Hour)
		for _, slug := range []string{"codex-6-1-sol-high", "codex-6-1-sol-xhigh", "claude-sonnet-high", "claude-opus-xhigh", "opencode-ollama-qwen3-coder-default"} {
			var id string
			if err := tx.QueryRow(ctx, `SELECT id::text FROM model_profiles WHERE slug=$1`, slug).Scan(&id); err != nil {
				return err
			}
			ids[slug] = id
		}
		if err := tx.QueryRow(ctx, `SELECT id::text FROM model_pref_profiles WHERE scope='workspace'`).Scan(&workspace); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank,set_by) VALUES($1,$2,'other','first',ARRAY['openai:sol'],$3)`, admin.TenantID, workspace, admin.ID); err != nil {
			return err
		}
		for role, slugs := range map[string][]string{
			"build":       {"codex-6-1-sol-high", "claude-sonnet-high", "opencode-ollama-qwen3-coder-default"},
			"build-hard":  {"claude-opus-xhigh", "codex-6-1-sol-xhigh"},
			"review-gate": {"codex-6-1-sol-xhigh", "claude-opus-xhigh"},
		} {
			for i, slug := range slugs {
				if _, err := tx.Exec(ctx, `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,$2,$3,$4)`, admin.TenantID, role, i+1, ids[slug]); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE model_role_routes SET state='conserved',reason='Owner pause',valid_until=$1 WHERE profile_id=$2 OR profile_id=$3`, until, ids["codex-6-1-sol-high"], ids["codex-6-1-sol-xhigh"]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_review_order(tenant_id,ordinary_review_order_mode) VALUES($1,'saved')`, admin.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_rules(tenant_id,scope,column_key,line,lock,position,why,set_by,set_at) VALUES
   ($1,'workspace','other','anthropic:sonnet','not',1,'Keep the owner rule',$2,$3),
   ($1,'workspace','backend','local:qwen-coder','bottom',2,'Last resort',$2,$3)`, admin.TenantID, admin.ID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agents.working','{"total":15,"daily":{"codex":{"pace":{"mode":"pace","points_per_day":null},"boost_today":null,"at_limit":"wait"}}}')`, admin.TenantID, waiter.ID); err != nil {
			return err
		}
		var own string
		if err := tx.QueryRow(ctx, `INSERT INTO model_pref_profiles(tenant_id,scope,person_id,set_by) VALUES($1,'person',$2,$2) RETURNING id::text`, admin.TenantID, waiter.ID).Scan(&own); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank,set_by) VALUES($1,$2,'other','first',ARRAY['openai:sol'],$3)`, admin.TenantID, own, waiter.ID); err != nil {
			return err
		}
		schedule := capacity.DefaultSchedule("UTC")
		schedule.Override = "sprint"
		schedule.OverrideUntil = &until
		schedule.Reserve = capacity.ReserveOff
		raw, _ := json.Marshal(schedule)
		for _, h := range []string{"codex", "claude", "opencode"} {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,state,last_probe_at,last_probe_ok,last_daemon_generation)
    VALUES($1,$2,$2,'migration-host',$3,$2,'available',$4,true,'migration-generation') RETURNING id::text`, admin.TenantID, h, runner.ID, now).Scan(&id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, admin.TenantID, admin.ID, id, raw); err != nil {
				return err
			}
		}
		return nil
	})
	// Fixed golden results from these saved ladders/rules before migration. The
	// oracle reads the retained source so wrong fixture setup cannot pass.
	inRegistry(t, admin, func(tx pgx.Tx) error {
		var eligible string
		if err := tx.QueryRow(t.Context(), `SELECT p.model FROM model_role_routes r JOIN model_profiles p ON p.id=r.profile_id
   WHERE r.role='build' AND (r.state='available' OR r.valid_until<=$1)
   AND NOT EXISTS(SELECT 1 FROM model_rules rule WHERE rule.lock='not' AND rule.column_key='other' AND rule.line=aeon_models_line(p.family,p.harness,p.model))
   ORDER BY r.priority LIMIT 1`, now).Scan(&eligible); err != nil {
			return err
		}
		if eligible != "ollama/qwen3-coder" {
			t.Fatalf("pre-migration golden fixture selected %q", eligible)
		}
		var waiting bool
		if err := tx.QueryRow(t.Context(), `SELECT cardinality(o.rank)=1 AND EXISTS(SELECT 1 FROM model_role_routes r JOIN model_profiles m ON m.id=r.profile_id WHERE r.role='build' AND aeon_models_line(m.family,m.harness,m.model)=o.rank[1] AND r.state<>'available' AND r.valid_until>$2) FROM model_pref_orders o JOIN model_pref_profiles own ON own.id=o.profile_id WHERE own.person_id=$1 AND o.column_key='other' AND o.situation='first'`, waiter.ID, now).Scan(&waiting); err != nil {
			return err
		}
		if !waiting {
			t.Fatal("pre-migration personal Wait fixture did not have a single held pick")
		}

		return nil
	})
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `SELECT aeon_migrate_all_models_v2()`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	inRegistry(t, admin, func(tx pgx.Tx) error {
		ctx := tenant.WithPrincipal(t.Context(), admin)
		for _, tc := range []struct {
			role, author, model string
			person              *string
		}{
			{"build", "", "ollama/qwen3-coder", nil},
			{"build-hard", "", "opus", nil},
			{"review-gate", "openai", "opus", nil},
			{"build", "", "", &waiter.ID},
		} {
			got, err := ResolveWork(ctx, tx, admin, WorkQuery{Role: tc.role, AuthorFamily: tc.author, Area: "other", Situation: "first", PersonID: tc.person}, now)
			if err != nil {
				return err
			}
			model := ""
			if got.Profile != nil {
				model = got.Profile.Model
			}
			if model != tc.model || tc.model == "" && !got.OwnerRequired {
				t.Fatalf("golden %s/person=%v: model=%q wait=%v trace=%+v", tc.role, tc.person, model, got.OwnerRequired, got.Trace)
			}
		}
		s, err := modelprefs.LoadBoard(ctx, tx, nil, "")
		if err != nil {
			return err
		}
		if len(s.Rules) != 2 {
			t.Fatalf("lost row constraints: %+v", s.Rules)
		}
		for _, rule := range s.Rules {
			if rule.Why == "Keep the owner rule" || rule.Why == "Last resort" {
				if rule.SetBy == nil || *rule.SetBy != admin.ID || !rule.SetAt.Equal(now) {
					t.Fatalf("rule ownership lost: %+v", rule)
				}
			}
		}
		var audit struct {
			Moved       []json.RawMessage `json:"moved"`
			Pauses      []json.RawMessage `json:"pauses"`
			Constraints []json.RawMessage `json:"constraints"`
			Situations  []json.RawMessage `json:"situations"`
		}
		if err := tx.QueryRow(ctx, `SELECT after FROM events WHERE type='model.settings_migrated'`).Scan(&audit); err != nil {
			return err
		}
		// Three ladders, three source holds, two rules and a saved Wait.
		if len(audit.Moved) != 9 || len(audit.Pauses) != 1 || len(audit.Constraints) != 2 || len(audit.Situations) != 1 {
			t.Fatalf("incomplete audit: %+v", audit)
		}
		var sourceCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_role_routes`).Scan(&sourceCount); err != nil {
			return err
		}
		if sourceCount != 7 {
			t.Fatalf("legacy rows changed: %d", sourceCount)
		}
		var before json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT audit FROM model_v2_migrations`).Scan(&before); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT aeon_migrate_all_models_v2()`); err != nil {
			return err
		}
		var eventCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='model.settings_migrated'`).Scan(&eventCount); err != nil {
			return err
		}
		if eventCount != 1 {
			t.Fatal("duplicate migration event")
		}
		var again json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT audit FROM model_v2_migrations`).Scan(&again); err != nil {
			return err
		}
		if len(before) == 0 || !reflect.DeepEqual(before, again) {
			t.Fatal("migration replay changed retained audit")
		}
		return nil
	})
}

// Risk: a forgotten runtime reader can silently reintroduce retired ladders.
func TestModelsV2NoRuntimeLadderReaders(t *testing.T) {
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, table := range []string{"model_role_routes", "model_security_role_routes"} {
			if strings.Contains(string(raw), table) {
				t.Errorf("runtime ladder reference in %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Risk: the deprecated endpoint still grants writes or exposes stale ladders.
func TestModelsV2RoutesRetiredAndDerived(t *testing.T) {
	admin, _ := modelsMigrationFixture(t)
	status, raw := call(t, &admin, http.MethodPut, "/api/models/routes", "not JSON")
	if status != http.StatusGone || !strings.Contains(string(raw), "moved") {
		t.Fatalf("retirement: %d %s", status, raw)
	}
	before := eventCount(t, admin, evRoutes)
	inRegistry(t, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank,effort,effort_level,end_mode)
   SELECT tenant_id,id,'other','first',ARRAY['anthropic:opus'],'xhigh',4,'wait' FROM model_pref_profiles WHERE scope='workspace'`)
		return err
	})
	got := decode[routesRead](t, &admin, http.MethodGet, "/api/models/routes?role=build", "", 200)
	if got.CanEdit || got.EditToken != nil || len(got.Steps) == 0 || got.Steps[0].Profile.Model != "opus" {
		t.Fatalf("not derived: %+v", got)
	}
	if before != eventCount(t, admin, evRoutes) {
		t.Fatal("deprecated endpoints wrote an event")
	}
}

// Risk: editing a line moves its pick but strands a fallback, situation,
// never rule or indefinite pause, or loses the source rule's stamps.
func TestModelsV2SuccessorMovesAllReferences(t *testing.T) {
	admin, _ := modelsMigrationFixture(t)
	inRegistry(t, admin, func(tx pgx.Tx) error {
		ctx := t.Context()
		var old Profile
		if err := tx.QueryRow(ctx, `SELECT id::text FROM model_profiles WHERE slug='claude-opus-xhigh'`).Scan(&old.ID); err != nil {
			return err
		}
		old.Harness, old.Family, old.Model, old.Effort = "claude", "anthropic", "opus", "xhigh"
		level := 4
		old.EffortLevel = &level
		next, err := insertProfile(ctx, tx, admin.TenantID, profileWrite{Slug: "successor-fable", Version: "test", Harness: "claude", Family: "anthropic", Model: "fable", Effort: "xhigh", Tier: "frontier"})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank,not_allowed,efforts)
   SELECT tenant_id,id,'other','first',ARRAY['openai:sol','anthropic:opus'],ARRAY['anthropic:opus'],'{"anthropic:opus":"xhigh"}' FROM model_pref_profiles WHERE scope='workspace'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_pref_situation_orders(tenant_id,profile_id,column_key,situation,rank,efforts,end_mode)
   SELECT tenant_id,id,'backend','review',ARRAY['anthropic:opus'],'{"anthropic:opus":"xhigh"}','wait' FROM model_pref_profiles WHERE scope='workspace'`); err != nil {
			return err
		}
		var at time.Time
		if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&at); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_row_constraints(tenant_id,scope,column_key,line,kind,why,set_by,set_at) VALUES($1,'workspace','backend','anthropic:opus','never','Keep this reason',$2,$3)`, admin.TenantID, admin.ID, at); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_pauses(tenant_id,harness,model,reason,set_by,set_at) VALUES($1,'claude','opus','Indefinite pause',$2,$3)`, admin.TenantID, admin.ID, at); err != nil {
			return err
		}
		if err := transferLineReferences(ctx, tx, admin, []Profile{old}, []Profile{next}); err != nil {
			return err
		}
		s, err := modelprefs.LoadBoard(ctx, tx, nil, "")
		if err != nil {
			return err
		}
		if len(s.Orders) != 2 {
			t.Fatalf("lost situations: %+v", s.Orders)
		}
		for _, o := range s.Orders {
			if o.Efforts["anthropic:fable"] != "xhigh" || o.Efforts["anthropic:opus"] != "" {
				t.Fatalf("stranded efforts: %+v", o)
			}
			if o.Situation == "review" && !reflect.DeepEqual(o.Rank, []string{"anthropic:fable"}) {
				t.Fatalf("stranded situation: %+v", o)
			}
			if o.Situation == "first" && !reflect.DeepEqual(o.Not, []string{"anthropic:fable"}) {
				t.Fatalf("stranded never: %+v", o)
			}
		}
		if len(s.Rules) != 1 || s.Rules[0].Line != "anthropic:fable" || s.Rules[0].SetBy == nil || *s.Rules[0].SetBy != admin.ID || !s.Rules[0].SetAt.Equal(at) {
			t.Fatalf("lost never stamps: %+v", s.Rules)
		}
		step, err := preferenceStep(ctx, tx, next, "build")
		if err != nil {
			return err
		}
		if step.State != "unavailable" || step.ValidUntil != nil || step.Reason != "Indefinite pause" {
			t.Fatalf("stranded pause: %+v", step)
		}
		return nil
	})
}
