// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"net/http/httptest"
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

// reviewOrderPut sends a review-order write to the retired ladder writer.
func reviewOrderPut(t *testing.T, h *editorHTTP, p tenant.Principal, mode string, rows []Route) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/models/routes?role=review-gate"
	if mode != "" {
		path += "&order_mode=" + mode
	}
	return h.call(t, p, "PUT", path, editorJSON(rows), map[string]string{"If-Match": `"` + strings.Repeat("2", 64) + `"`})
}

// Risk: the retired ladder writer activates or resets the managed review
// order, or changes rows or events. A saved Models Review situation is the
// managed order now; retained mode metadata stays tenant-isolated.
func TestReviewOrderActivationTokenAuditUndoAndLegacyPreservation(t *testing.T) {
	p, h := editorFixture(t)
	before := h.ladder(t, p, "review-gate")
	if before.OrderMode != "legacy" || len(before.Steps) == 0 || len(before.ManagedFallbackOrder) != len(before.Steps) || before.EditToken != nil || before.CanEdit {
		t.Fatal("missing automatic review snapshot")
	}
	n, state := eventCount(t, p, evRoutes), catalogSetupState(t, p)
	for _, mode := range []string{"", "saved", "legacy", "unknown"} {
		editorRetired(t, reviewOrderPut(t, h, p, mode, before.Routes))
	}
	editorRetired(t, h.call(t, p, "PUT", "/api/models/routes?order_mode=saved", "[]", nil))
	if eventCount(t, p, evRoutes) != n || catalogSetupState(t, p) != state || h.ladder(t, p, "review-gate").OrderMode != "legacy" {
		t.Fatal("retired writer activated managed order or wrote")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return dbtest.SetModelOrder(t.Context(), tx, p.TenantID, "", "other", "review", before.ManagedFallbackOrder...)
	}); err != nil {
		t.Fatal(err)
	}
	if saved := h.ladder(t, p, "review-gate"); saved.OrderMode != "saved" || saved.EditToken != nil || saved.CanEdit || len(saved.Steps) == 0 {
		t.Fatal("saved Review situation is not the managed order", saved)
	}
	otherTenant := makePrincipal(t, "order-isolation", "person", "Other", []string{"admin"})
	empty := h.ladder(t, otherTenant, "review-gate")
	if empty.OrderMode != "legacy" || empty.Setup {
		t.Fatal("mode crossed tenant or initialized registry")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, otherTenant.TenantID, func(tx pgx.Tx) error {
		expectConstraint(t, tx, "42501", `INSERT INTO model_review_order(tenant_id,ordinary_review_order_mode) VALUES($1,'saved')`, p.TenantID)
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_review_order`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("RLS exposed ordering metadata")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Risk: a review-order write to the retired ladder writer still reaches the
// event insert, leaving a partial mode or rows behind an audit failure.
func TestReviewOrderEventFailureRollsBackRowsAndMode(t *testing.T) {
	p, h := editorFixture(t)
	before := h.ladder(t, p, "review-gate")
	n, state := eventCount(t, p, evRoutes), catalogSetupState(t, p)
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
		editorRetired(t, reviewOrderPut(t, h, p, "saved", rows))
		after := h.ladder(t, p, "review-gate")
		if after.OrderMode != before.OrderMode || !reflect.DeepEqual(after.Routes, before.Routes) || eventCount(t, p, evRoutes) != n || catalogSetupState(t, p) != state {
			t.Fatal("retired writer changed mode, rows or events")
		}
	}
	var reached bool
	if err := adminPool.QueryRow(t.Context(), `SELECT is_called FROM review_order_probe`).Scan(&reached); err != nil || reached {
		t.Fatal("retired writer reached the event insert", err, reached)
	}
}

// One qualified Claude account, with a strong profile before a frontier profile.
// Saved mode can change their fallback rank, never their qualification.
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
	if _, err := tx.Exec(t.Context(), `DELETE FROM model_role_routes WHERE role='review-gate'`); err != nil {
		t.Fatal(err)
	}
	for i, profile := range []Profile{strong, frontier} {
		if err := insertRoute(t.Context(), tx, p.TenantID, Route{Role: "review-gate", Priority: i + 1, ProfileID: profile.ID, State: "available"}); err != nil {
			t.Fatal(err)
		}
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

// Risk: a saved Models Review order changes which qualified reviewer runs
// first but qualifies a profile it lists: an unqualified, disabled, retired,
// author-family, unsupported-adapter, filtered, accountless or non-resident
// choice must stay refused with its cause.
func TestReviewOrderSavedFallbackPreferencesAndQualification(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		strong, frontier := reviewOrderProfiles(t, tx, p)
		// Only the fixture pins have an account; catalog lines stay unrunnable.
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET allowed_model_profile_ids=ARRAY[$1::uuid,$2::uuid] WHERE account_key='order'`, strong.ID, frontier.ID); err != nil {
			return err
		}
		now := time.Now()
		q := WorkQuery{AuthorFamily: "openai"}
		automatic, err := ResolveReviewFor(t.Context(), tx, p, q, now)
		if err != nil {
			return err
		}
		if automatic.Profile == nil || automatic.Profile.ID != frontier.ID || automatic.Trace.OrderMode != "legacy" {
			t.Fatal("automatic frontier/tier order changed", automatic)
		}
		if err := dbtest.SetModelOrder(t.Context(), tx, p.TenantID, "", "other", "review", strong.ID, frontier.ID); err != nil {
			return err
		}
		saved, err := ResolveReviewFor(t.Context(), tx, p, q, now)
		if err != nil {
			return err
		}
		if saved.Profile == nil || saved.Profile.ID != strong.ID || saved.Trace.OrderMode != "saved" || saved.Ladder[0].ProfileID != strong.ID {
			t.Fatal("saved order not applied", saved)
		}
		if err := dbtest.SetModelOrder(t.Context(), tx, p.TenantID, "", "other", "review", frontier.ID, strong.ID); err != nil {
			return err
		}
		preferred, err := ResolveReviewFor(t.Context(), tx, p, q, now)
		if err != nil {
			return err
		}
		if preferred.Profile == nil || preferred.Profile.ID != frontier.ID || !preferred.Ladder[0].Selected || preferred.Trace.OrderMode != "saved" {
			t.Fatal("saved order lost qualified precedence", preferred)
		}
		const notRoute = "no registered route for this line"
		for _, tc := range []struct {
			name, sql, reason string
			query             WorkQuery
		}{
			{"effort", "", "review requires strong or frontier at the highest native level", q},
			{"tier", "", "review requires strong or frontier at the highest native level", q},
			{"disabled", "", notRoute, q},
			{"retired", `INSERT INTO model_profile_retirements(tenant_id,profile_id,reason,retired_by) VALUES($2,$1,'order-test',$3)`, "retired", q},
			{"author", "", "author family", WorkQuery{AuthorFamily: "anthropic"}},
			{"adapter", "", "Codex read-only sandboxing does not isolate inherited MCP tools and startup hooks.", WorkQuery{AuthorFamily: "xai"}},
			{"harness", "", "harness filter", WorkQuery{AuthorFamily: "openai", Harness: "codex"}},
			{"account", `UPDATE agent_accounts SET last_probe_ok=false`, "Claude account cannot take new work right now", q},
			{"residency", "", "not allowed: eu", WorkQuery{AuthorFamily: "openai", TicketResidency: "eu"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				sub, err := tx.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer sub.Rollback(t.Context())
				target := frontier
				if tc.name == "effort" || tc.name == "tier" || tc.name == "disabled" || tc.name == "adapter" {
					effort, tier := frontier.Effort, frontier.Tier
					if tc.name == "effort" {
						effort = "high"
					}
					if tc.name == "tier" {
						tier = "fast"
					}
					harness, family, model := "claude", "anthropic", "opus-test"
					if tc.name == "adapter" {
						harness, family, model = "codex", "openai", "gpt-6-sol"
					}
					if err := sub.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier,enabled,registered_effort_level) VALUES($1,$2,'1',$6,$7,$8,$3,$4,$5,CASE $3 WHEN 'xhigh' THEN 4 ELSE 3 END) RETURNING id::text`, p.TenantID, "unqualified-"+tc.name, effort, tier, tc.name != "disabled", harness, family, model).Scan(&target.ID); err != nil {
						t.Fatal(err)
					}
					target.Harness, target.Family, target.Model = harness, family, model
				}
				// The saved order lists the target first.
				order := []string{target.ID, strong.ID}
				if err := dbtest.SetModelOrder(t.Context(), sub, p.TenantID, "", "other", "review", order...); err != nil {
					t.Fatal(err)
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
				if got.Profile != nil && got.Profile.ID == target.ID {
					t.Fatal("saved unqualified profile escaped", tc.name)
				}
				reasons := []string{}
				found := false
				for _, c := range got.Ladder {
					if c.ProfileID == target.ID {
						found = true
						if c.Selected {
							t.Fatal("saved unqualified profile selected", tc.name)
						}
						reasons = append(reasons, c.SkipReasons...)
					}
				}
				if !found {
					// A choice without a dispatchable route is held at its line.
					line, _ := modelsLine(target)
					for _, held := range got.Trace.Held {
						if held.Line == line {
							reasons = append(reasons, held.Reason)
						}
					}
				}
				if !strings.Contains(strings.Join(reasons, ";"), tc.reason) {
					t.Fatal("wrong qualifying refusal", tc.name, reasons, got)
				}
			})
		}
		security, err := ResolveReviewFor(t.Context(), tx, p, WorkQuery{AuthorFamily: "openai", Area: "security"}, now)
		if err != nil {
			return err
		}
		if !security.OwnerRequired || security.Profile != nil || security.Role != "review-gate-security" || len(security.Trace.Hard) != 1 || security.Trace.Hard[0] != "security_review" || security.Trace.Blocked != "no qualified security review profile/account" {
			t.Fatal("saved order escaped security path", security)
		}
		return nil
	})
}

// Risk: the compatibility read or the resolver mixes a saved Models Review
// order with its previous state, or reads it without waiting for the writer
// that holds it. Both wait for the commit and report the saved order.
func TestReviewOrderReadAndResolverSnapshotBarrier(t *testing.T) {
	for _, resolver := range []bool{false, true} {
		t.Run(map[bool]string{false: "GET", true: "resolver"}[resolver], func(t *testing.T) {
			p, h := editorFixture(t)
			var strong, frontier Profile
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error { strong, frontier = reviewOrderProfiles(t, tx, p); return nil }); err != nil {
				t.Fatal(err)
			}
			writer, err := adminPool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = writer.Rollback(context.Background()) })
			// Hold every Models preference table the readers load, so the first
			// such read waits for the whole saved order.
			if _, err := writer.Exec(t.Context(), `LOCK TABLE model_pref_profiles, model_pref_orders, model_pref_situation_orders IN ACCESS EXCLUSIVE MODE`); err != nil {
				t.Fatal(err)
			}
			var pid int
			if err := writer.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Exec(t.Context(), `SELECT set_config('aeon.tenant_id',$1,true)`, p.TenantID); err != nil {
				t.Fatal(err)
			}
			if err := dbtest.SetModelOrder(t.Context(), writer, p.TenantID, "", "other", "review", strong.ID, frontier.ID); err != nil {
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
			} else if view.OrderMode != "saved" || view.EditToken != nil || len(view.ManagedFallbackOrder) < 2 || view.ManagedFallbackOrder[0] != strong.ID || view.ManagedFallbackOrder[1] != frontier.ID {
				t.Fatal("GET mixed order snapshot", view)
			}
		})
	}
}

// Risk: a review-order write to the retired ladder writer waits behind the
// tenant fence or commits; after demotion the route permission refuses it.
func TestReviewOrderFinalRevocationAndConcurrentCAS(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "mode CAS", true: "revoke"}[revoke], func(t *testing.T) {
			p, h := editorFixture(t)
			before := h.ladder(t, p, "review-gate")
			check := editorRetired
			if revoke {
				if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='member') WHERE principal_id=$1 AND scope_type='workspace'`, p.ID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				check = func(t *testing.T, w *httptest.ResponseRecorder) {
					t.Helper()
					if w.Code != 403 {
						t.Fatalf("demoted person reached the retired writer: %d %s", w.Code, w.Body.String())
					}
				}
			}
			retiredWriteWhileFenced(t, p, "/api/models/routes?role=review-gate&order_mode=saved", editorJSON(before.Routes), map[string]string{"If-Match": `"` + strings.Repeat("3", 64) + `"`}, check)
			if h.ladder(t, p, "review-gate").OrderMode != before.OrderMode {
				t.Fatal("refused mode write changed the order mode")
			}
		})
	}
}

// Catalog v3 must distinguish an explicitly saved v2 default from an untouched
// fallback; equality with the historical rows does not undo opt-in ownership.
func TestReviewOrderCatalogUpgradePreservesSavedV2Default(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "saved-v2-upgrade", "person", "Owner", []string{"admin"})
	inRegistry(t, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_refresh_settings(tenant_id,catalog_version) VALUES($1,'2')`, p.TenantID); err != nil {
			return err
		}
		slugs := append([]string(nil), v2Ladders["review-gate"]...)
		slugs = append(slugs, "gemini-gemini-2-5-pro-32768", "opencode-google-gemini-2-5-pro-default")
		for i, slug := range slugs {
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
			if err := insertRoute(t.Context(), tx, p.TenantID, Route{Role: "review-gate", Priority: i + 1, ProfileID: profile.ID, State: "available"}); err != nil {
				return err
			}
		}
		return saveReviewOrder(t.Context(), tx, p.TenantID, reviewOrderSaved)
	})
	// Retained ladder rows are history for the deprecation release; the
	// upgrade neither rewrites them nor turns them into Models orders.
	retained := func() string {
		var state string
		inRegistry(t, p, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'ladder',(SELECT jsonb_agg(jsonb_build_array(priority,profile_id,state) ORDER BY priority) FROM model_role_routes WHERE role='review-gate'),
 'orders',(SELECT count(*) FROM model_pref_orders)+(SELECT count(*) FROM model_pref_situation_orders))::text`).Scan(&state)
		})
		return state
	}
	before := retained()
	var rows int
	inRegistry(t, p, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM model_role_routes WHERE role='review-gate'`).Scan(&rows)
	})
	if rows != 6 {
		t.Fatal("historical default fixture lost its rows")
	}
	decode[[]Profile](t, &p, "GET", "/api/models", "", 200)
	if after := retained(); after != before {
		t.Fatalf("catalog upgrade replaced saved review history: before=%s after=%s", before, after)
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
