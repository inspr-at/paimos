// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// reviewOrderSave saves the workspace Review order, Models v2's saved review
// fallback (a saved-mode review ladder before). No profiles clears it, so
// reviews return to Automatic (the former legacy mode).
func reviewOrderSave(t *testing.T, tx pgx.Tx, p tenant.Principal, profiles ...string) error {
	t.Helper()
	if len(profiles) == 0 {
		_, err := tx.Exec(t.Context(), `DELETE FROM model_pref_situation_orders WHERE column_key='other' AND situation='review'`)
		return err
	}
	return dbtest.ModelsOrder(t.Context(), tx, p.TenantID, "other", "review", "wait", profiles...)
}

// Risk: the retired ladder writer can still activate or undo a saved review
// order, or the deprecated read misreports which one applies. It reports
// "saved" exactly while a Review order exists and "legacy" (Automatic)
// otherwise; every former activation write is 410 and changes nothing.
func TestReviewOrderActivationTokenAuditUndoAndLegacyPreservation(t *testing.T) {
	p, h := editorFixture(t)
	var strong, frontier Profile
	inRegistry(t, p, func(tx pgx.Tx) error { strong, frontier = reviewOrderProfiles(t, tx, p); return nil })
	before := h.ladder(t, p, "review-gate")
	other := h.ladder(t, p, "build")
	if before.OrderMode != "legacy" || len(before.ManagedFallbackOrder) != len(before.Routes) || before.EditToken != nil {
		t.Fatal("missing Automatic snapshot", before)
	}
	state := modelsState(t, p)
	for _, q := range []string{"?role=review-gate", "?role=review-gate&order_mode=saved", "?role=review-gate&order_mode=legacy", "", "?order_mode=saved", "?role=build&order_mode=saved", "?role=review-gate&order_mode=unknown", "?role=review-gate&order_mode=saved&order_mode=legacy"} {
		retiredLadderWrite(t, h, p, "/api/models/routes"+q, editorJSON(before.Routes), map[string]string{"If-Match": routeEditToken("review-gate", before.Routes, "saved")})
	}
	if modelsState(t, p) != state || h.ladder(t, p, "review-gate").OrderMode != "legacy" {
		t.Fatal("retired writer activated a saved order")
	}
	// The successor: save a Review order, then remove it again (Undo).
	inRegistry(t, p, func(tx pgx.Tx) error { return reviewOrderSave(t, tx, p, frontier.ID, strong.ID) })
	saved := h.ladder(t, p, "review-gate")
	if saved.OrderMode != "saved" || !reflect.DeepEqual(saved.ManagedFallbackOrder, []string{frontier.ID, strong.ID}) || saved.EditToken != nil {
		t.Fatal("saved Review order not reported", saved)
	}
	if !reflect.DeepEqual(h.ladder(t, p, "build").Routes, other.Routes) {
		t.Fatal("Review order changed an unrelated role")
	}
	inRegistry(t, p, func(tx pgx.Tx) error { return reviewOrderSave(t, tx, p) })
	restored := h.ladder(t, p, "review-gate")
	if restored.OrderMode != "legacy" || !reflect.DeepEqual(restored.Routes, before.Routes) {
		t.Fatal("removing the Review order did not restore Automatic", restored)
	}
	otherTenant := makePrincipal(t, "order-isolation", "person", "Other", []string{"admin"})
	empty := h.ladder(t, otherTenant, "review-gate")
	if empty.OrderMode != "legacy" || empty.Setup {
		t.Fatal("order crossed tenant or initialized registry")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, otherTenant.TenantID, func(tx pgx.Tx) error {
		expectConstraint(t, tx, "42501", `INSERT INTO model_review_order(tenant_id,ordinary_review_order_mode) VALUES($1,'saved')`, p.TenantID)
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM model_review_order)+(SELECT count(*) FROM model_pref_situation_orders)`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("RLS exposed review ordering")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Risk: a refused activation leaves part of a change behind. The retired
// writer never reaches the event insert, so nothing can be half-written.
func TestReviewOrderEventFailureRollsBackRowsAndMode(t *testing.T) {
	p, h := editorFixture(t)
	before := h.ladder(t, p, "review-gate")
	state := modelsState(t, p)
	if _, err := adminPool.Exec(t.Context(), `CREATE SEQUENCE review_order_probe; CREATE FUNCTION review_order_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='model.routes_replaced' THEN PERFORM nextval('review_order_probe'); RAISE EXCEPTION 'review order injected event failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER review_order_fail BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION review_order_fail()`); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(t.Context(), `GRANT USAGE ON SEQUENCE review_order_probe TO `+pgx.Identifier{testDB.Role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := adminPool.Exec(context.Background(), `DROP TRIGGER review_order_fail ON events; DROP FUNCTION review_order_fail(); DROP SEQUENCE review_order_probe`); err != nil {
			t.Error(err)
		}
	}()
	for _, reorder := range []bool{false, true} {
		rows := append([]Route{}, before.Routes...)
		if reorder && len(rows) > 0 {
			rows[0].Priority = 500
		}
		retiredLadderWrite(t, h, p, "/api/models/routes?role=review-gate&order_mode=saved", editorJSON(rows), map[string]string{"If-Match": routeEditToken("review-gate", before.Routes, "legacy")})
		after := h.ladder(t, p, "review-gate")
		if after.OrderMode != "legacy" || !reflect.DeepEqual(after.Routes, before.Routes) || modelsState(t, p) != state {
			t.Fatal("partial mode or rows after a refused activation")
		}
	}
	var reached bool
	if err := adminPool.QueryRow(t.Context(), `SELECT is_called FROM review_order_probe`).Scan(&reached); err != nil || reached {
		t.Fatal("retired writer reached the event insert", err, reached)
	}
}

// One qualified Claude account, with a strong and a frontier profile on lines
// of their own. A saved Review order can change their rank, never their
// qualification.
func reviewOrderProfiles(t *testing.T, tx pgx.Tx, p tenant.Principal) (Profile, Profile) {
	t.Helper()
	strong, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{Slug: "order-strong", Version: "1", Harness: "claude", Family: "anthropic", Model: "opus-order-strong", Effort: "xhigh", Tier: "strong"})
	if err != nil {
		t.Fatal(err)
	}
	frontier, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{Slug: "order-frontier", Version: "1", Harness: "claude", Family: "anthropic", Model: "opus-order-frontier", Effort: "xhigh", Tier: "frontier"})
	if err != nil {
		t.Fatal(err)
	}
	var runner, account string
	if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Order runner') RETURNING id::text`, p.TenantID).Scan(&runner); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE key='admin'`, p.TenantID, runner); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner) VALUES($1,'order','claude','order-runner',$2,'Order',clock_timestamp(),true,'order-generation',$3) RETURNING id::text`, p.TenantID, runner, p.ID).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model) VALUES($1,$2,now()-interval '1 hour',now()+interval '1 day','tokens',10000000,'unrestricted')`, p.TenantID, account); err != nil {
		t.Fatal(err)
	}
	schedule := capacity.DefaultSchedule()
	for i := range schedule.Week {
		schedule.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	schedule.Override = "sprint"
	schedule.Reserve = capacity.ReserveOff
	raw, _ := json.Marshal(schedule)
	if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, p.TenantID, p.ID, account, raw); err != nil {
		t.Fatal(err)
	}
	return strong, frontier
}

func TestReviewOrderSavedFallbackPreferencesAndQualification(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		strong, frontier := reviewOrderProfiles(t, tx, p)
		now := time.Now()
		q := WorkQuery{AuthorFamily: "openai"}
		automatic, err := ResolveReviewFor(t.Context(), tx, p, q, now)
		if err != nil {
			return err
		}
		// Automatic takes the strongest qualified model, never the strong one first.
		if automatic.Profile == nil || automatic.Profile.Tier != "frontier" || automatic.Trace.OrderMode != "legacy" {
			t.Fatal("Automatic frontier/tier fallback changed", automatic)
		}
		if err := reviewOrderSave(t, tx, p, strong.ID, frontier.ID); err != nil {
			return err
		}
		saved, err := ResolveReviewFor(t.Context(), tx, p, q, now)
		if err != nil {
			return err
		}
		if saved.Profile == nil || saved.Profile.ID != strong.ID || saved.Trace.OrderMode != "saved" || saved.Ladder[0].ProfileID != strong.ID {
			t.Fatal("saved fallback not applied", saved)
		}
		// The former preferred pick is the Review order's first step.
		if err := reviewOrderSave(t, tx, p, frontier.ID, strong.ID); err != nil {
			return err
		}
		preferred, err := ResolveReviewFor(t.Context(), tx, p, q, now)
		if err != nil {
			return err
		}
		if preferred.Profile == nil || preferred.Profile.ID != frontier.ID || !preferred.Ladder[0].Selected || preferred.Trace.OrderMode != "saved" {
			t.Fatal("preference lost qualified precedence", preferred)
		}
		for _, tc := range []struct {
			name, sql, reason string
			query             WorkQuery
		}{
			{"effort", "", "review requires strong or frontier at the highest native level", q},
			{"tier", "", "review requires strong or frontier at the highest native level", q},
			{"disabled", "", "policy", q},
			{"retired", `INSERT INTO model_profile_retirements(tenant_id,profile_id,reason,retired_by) VALUES($2,$1,'order-test',$3)`, "retired", q},
			{"author", "", "author family", WorkQuery{AuthorFamily: "anthropic"}},
			{"adapter", "", "Codex read-only sandboxing does not isolate inherited MCP tools and startup hooks.", WorkQuery{AuthorFamily: "xai"}},
			{"harness", "", "harness filter", WorkQuery{AuthorFamily: "openai", Harness: "codex"}},
			{"account", `UPDATE agent_accounts SET last_probe_ok=false`, "no qualified account with available capacity", q},
			{"residency", "", "no qualified account with available capacity", WorkQuery{AuthorFamily: "openai", TicketResidency: "eu"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				sub, err := tx.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer sub.Rollback(t.Context())
				target := frontier.ID
				if tc.name == "effort" || tc.name == "tier" || tc.name == "disabled" || tc.name == "adapter" {
					effort, tier := frontier.Effort, frontier.Tier
					if tc.name == "effort" {
						effort = "high"
					}
					if tc.name == "tier" {
						tier = "fast"
					}
					// Lines of their own, so no catalog route stands in for them.
					harness, family, model := "claude", "anthropic", "opus-test"
					if tc.name == "adapter" {
						harness, family, model = "codex", "openai", "gpt-6-ordertest"
					}
					level := 4
					if tc.name == "effort" {
						level = 3
					}
					if err := sub.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier,enabled,registered_effort_level) VALUES($1,$2,'1',$6,$7,$8,$3,$4,$5,$9) RETURNING id::text`, p.TenantID, "unqualified-"+tc.name, effort, tier, tc.name != "disabled", harness, family, model, level).Scan(&target); err != nil {
						t.Fatal(err)
					}
					if err := reviewOrderSave(t, sub, p, target, strong.ID); err != nil {
						t.Fatal(err)
					}
				}
				if tc.sql != "" {
					var err error
					if tc.name == "retired" {
						_, err = sub.Exec(t.Context(), tc.sql, frontier.ID, p.TenantID, p.ID)
					} else {
						_, err = sub.Exec(t.Context(), tc.sql)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				got, err := ResolveReviewFor(t.Context(), sub, p, tc.query, now)
				if err != nil {
					t.Fatal(err)
				}
				if got.Profile != nil && got.Profile.ID == target {
					t.Fatal("preferred unqualified profile escaped", tc.name)
				}
				var candidate Candidate
				for _, c := range got.Ladder {
					if c.ProfileID == target {
						candidate = c
					}
				}
				if !strings.Contains(strings.Join(candidate.SkipReasons, ";"), tc.reason) || candidate.Selected {
					t.Fatal("wrong qualifying refusal", tc.name, candidate, got.Ladder)
				}
			})
		}
		// Security reviews read the same Review order, never Anthropic.
		security, err := ResolveReviewFor(t.Context(), tx, p, WorkQuery{AuthorFamily: "openai", Area: "security"}, now)
		if err != nil {
			return err
		}
		if !security.OwnerRequired || security.Profile != nil || security.Trace.OrderMode != "saved" || security.Role != "review-gate-security" || len(security.Trace.Hard) != 1 || security.Trace.Hard[0] != "security_review" {
			t.Fatal("saved order escaped security path", security)
		}
		return nil
	})
}

// Risk: a read overlapping a Review order save mixes the old and new order.
// The read waits on the order write and then sees the complete new order.
func TestReviewOrderReadAndResolverSnapshotBarrier(t *testing.T) {
	for _, resolver := range []bool{false, true} {
		t.Run(map[bool]string{false: "GET", true: "resolver"}[resolver], func(t *testing.T) {
			p, h := editorFixture(t)
			var strong, frontier Profile
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				strong, frontier = reviewOrderProfiles(t, tx, p)
				// The workspace profile exists before the barrier, so only the order changes.
				_, err := tx.Exec(t.Context(), `INSERT INTO model_pref_profiles(tenant_id,scope,template,thinking,usage) SELECT $1,'workspace','balanced','standard','balanced' WHERE NOT EXISTS(SELECT 1 FROM model_pref_profiles WHERE scope='workspace')`, p.TenantID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			writer, pid := lockModelsTable(t, "model_pref_situation_orders")
			if _, err := writer.Exec(t.Context(), `SELECT set_config('aeon.tenant_id',$1,true)`, p.TenantID); err != nil {
				t.Fatal(err)
			}
			if err := dbtest.ModelsOrder(t.Context(), writer, p.TenantID, "other", "review", "wait", strong.ID, frontier.ID); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			var route ReviewRoute
			var view routesRead
			go func() {
				if resolver {
					result <- db.InTenant(t.Context(), appPool, p.TenantID, func(tx pgx.Tx) error {
						var err error
						route, err = ResolveReviewFor(t.Context(), tx, p, WorkQuery{AuthorFamily: "openai"}, time.Now())
						return err
					})
				} else {
					w := h.call(t, p, "GET", "/api/models/routes?role=review-gate", "", nil)
					if w.Code != 200 {
						result <- fail(w.Code, w.Body.String())
						return
					}
					result <- json.Unmarshal(w.Body.Bytes(), &view)
				}
			}()
			waitEditorBlocked(t, pid)
			if err := writer.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("snapshot reader hung")
			}
			if resolver {
				if route.Trace.OrderMode != "saved" || route.Profile == nil || route.Profile.ID != strong.ID || route.Ladder[0].ProfileID != strong.ID {
					t.Fatal("resolver mixed order snapshot", route)
				}
			} else if view.OrderMode != "saved" || view.EditToken != nil || !reflect.DeepEqual(view.ManagedFallbackOrder, []string{strong.ID, frontier.ID}) {
				t.Fatal("GET mixed order snapshot", view)
			}
		})
	}
}

// Risk: the retired activation still waits on the tenant fence, or a refused
// activation (stale or revoked) changes the order. It is 410 at once.
func TestReviewOrderFinalRevocationAndConcurrentCAS(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "mode CAS", true: "revoke"}[revoke], func(t *testing.T) {
			p, h := editorFixture(t)
			before := h.ladder(t, p, "review-gate")
			state := modelsState(t, p)
			blocker, err := appPool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err := blocker.Exec(t.Context(), `SELECT set_config('aeon.tenant_id',$1,true)`, p.TenantID); err != nil {
				t.Fatal(err)
			}
			if err := db.LockTenant(t.Context(), blocker, p.TenantID); err != nil {
				t.Fatal(err)
			}
			if revoke {
				_, err = blocker.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='member') WHERE principal_id=$1 AND scope_type='workspace'`, p.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			// Answered while the fence is held: the retired writer waits for nothing.
			w := finishEditorRequest(t, startEditorRequest(t, h, p, "PUT", "/api/models/routes?role=review-gate&order_mode=saved", editorJSON(before.Routes), map[string]string{"If-Match": routeEditToken("review-gate", before.Routes, "legacy")}))
			if err := blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if w.Code != 410 {
				t.Fatalf("retired activation answered %d: %s", w.Code, w.Body.String())
			}
			if h.ladder(t, p, "review-gate").OrderMode != "legacy" || modelsState(t, p) != state {
				t.Fatal("refused activation changed the order or events")
			}
		})
	}
}

// Risk: the catalog upgrade replaces a saved Review order that equals the
// historical default; equality does not undo the workspace's choice.
func TestReviewOrderCatalogUpgradePreservesSavedV2Default(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "saved-v2-upgrade", "person", "Owner", []string{"admin"})
	inRegistry(t, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_refresh_settings(tenant_id,catalog_version) VALUES($1,'2')`, p.TenantID); err != nil {
			return err
		}
		slugs := []string{"codex-astra-xhigh", "claude-fable-xhigh", "claude-opus-xhigh", "cursor-grok-xhigh", "gemini-gemini-2-5-pro-32768", "opencode-google-gemini-2-5-pro-default"}
		ids := []string{}
		for _, slug := range slugs {
			var seed seedProfile
			for _, candidate := range catalogProfiles() {
				if candidate.Slug == slug {
					seed = candidate
				}
			}
			if seed.Slug == "" {
				t.Fatalf("missing historical seed %s", slug)
			}
			profile, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{Slug: slug, Version: "2", Harness: seed.Harness, Family: seed.Family, Model: seed.Model, Effort: seed.Effort, Tier: seed.Tier})
			if err != nil {
				return err
			}
			ids = append(ids, profile.ID)
		}
		if err := reviewOrderSave(t, tx, p, ids...); err != nil {
			return err
		}
		return saveReviewOrder(t.Context(), tx, p.TenantID, reviewOrderSaved)
	})
	order := func() string {
		t.Helper()
		var rank string
		inRegistry(t, p, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT jsonb_build_array(rank,efforts,end_mode)::text FROM model_pref_situation_orders WHERE column_key='other' AND situation='review'`).Scan(&rank)
		})
		return rank
	}
	before := order()
	decode[[]Profile](t, &p, "GET", "/api/models", "", 200)
	if after := order(); after != before {
		t.Fatalf("catalog upgrade replaced saved Review order: before=%s after=%s", before, after)
	}
	inRegistry(t, p, func(tx pgx.Tx) error {
		mode, err := storedReviewOrder(t.Context(), tx)
		if err != nil {
			return err
		}
		if mode != reviewOrderSaved {
			t.Fatal("catalog upgrade deactivated saved review mode")
		}
		return requireCatalog(t.Context(), tx)
	})
	if eventCount(t, p, "model.catalog_upgraded") != 1 {
		t.Fatal("catalog upgrade did not run")
	}
}
