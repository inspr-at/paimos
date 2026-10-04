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
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func reviewOrderPut(t *testing.T, h *editorHTTP, p tenant.Principal, before routesRead, mode string, rows []Route) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/models/routes?role=review-gate"
	if mode != "" {
		path += "&order_mode=" + mode
	}
	return h.call(t, p, "PUT", path, editorJSON(rows), map[string]string{"If-Match": *before.EditToken})
}

func TestReviewOrderActivationTokenAuditUndoAndLegacyPreservation(t *testing.T) {
	p, h := editorFixture(t)
	before := h.ladder(t, p, "review-gate")
	other := h.ladder(t, p, "build")
	if before.OrderMode != "legacy" || len(before.ManagedFallbackOrder) != len(before.Routes) {
		t.Fatal("missing legacy snapshot")
	}
	n := eventCount(t, p, evRoutes)
	// A rows-only save, including a legacy write, never activates managed order.
	editorDecode[[]Route](t, reviewOrderPut(t, h, p, before, "", before.Routes))
	if eventCount(t, p, evRoutes) != n || h.ladder(t, p, "review-gate").OrderMode != "legacy" {
		t.Fatal("implicit activation")
	}
	w := reviewOrderPut(t, h, p, before, "saved", before.Routes)
	editorDecode[[]Route](t, w)
	saved := h.ladder(t, p, "review-gate")
	if saved.OrderMode != "saved" || *saved.EditToken == *before.EditToken || w.Header().Get("ETag") != *saved.EditToken || w.Header().Get("Model-Order-Mode") != "saved" || eventCount(t, p, evRoutes) != n+1 || !reflect.DeepEqual(before.Routes, saved.Routes) {
		t.Fatal("mode-only activation not atomic")
	}
	if *h.ladder(t, p, "build").EditToken != *other.EditToken {
		t.Fatal("mode invalidated unrelated role")
	}
	editorError(t, reviewOrderPut(t, h, p, before, "legacy", before.Routes), 409, "stale_revision")
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var actor string
		var prev, next []Route
		var meta map[string]string
		if err := tx.QueryRow(t.Context(), `SELECT actor_principal_id::text,before,after,metadata FROM events WHERE type=$1 ORDER BY id DESC LIMIT 1`, evRoutes).Scan(&actor, &prev, &next, &meta); err != nil {
			return err
		}
		if actor != p.ID || !reflect.DeepEqual(prev, next) || len(prev) < len(before.Routes) || meta["before_order_mode"] != "legacy" || meta["after_order_mode"] != "saved" {
			t.Fatal("audit lost actor, array shape or mode")
		}
		rows, err := listRoutes(t.Context(), tx)
		if err != nil {
			return err
		}
		all := h.call(t, p, "PUT", "/api/models/routes", editorJSON(rows), nil)
		editorDecode[[]Route](t, all)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if h.ladder(t, p, "review-gate").OrderMode != "saved" {
		t.Fatal("legacy PUT reset activation")
	}
	editorDecode[[]Route](t, reviewOrderPut(t, h, p, saved, "", saved.Routes))
	if eventCount(t, p, evRoutes) != n+1 {
		t.Fatal("preserved-mode no-op emitted event")
	}
	editorDecode[[]Route](t, reviewOrderPut(t, h, p, saved, "legacy", before.Routes))
	restored := h.ladder(t, p, "review-gate")
	if restored.OrderMode != "legacy" || *restored.EditToken != *before.EditToken || eventCount(t, p, evRoutes) != n+2 {
		t.Fatal("rows+mode Undo failed")
	}
	editorError(t, reviewOrderPut(t, h, p, saved, "saved", saved.Routes), 409, "stale_revision")
	for _, q := range []string{"order_mode=saved", "role=build&order_mode=saved", "role=review-gate&order_mode=unknown", "role=review-gate&order_mode=saved&order_mode=legacy"} {
		editorError(t, h.call(t, p, "PUT", "/api/models/routes?"+q, "[]", map[string]string{"If-Match": *restored.EditToken}), 400, "invalid_order_mode")
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

func TestReviewOrderEventFailureRollsBackRowsAndMode(t *testing.T) {
	p, h := editorFixture(t)
	before := h.ladder(t, p, "review-gate")
	n := eventCount(t, p, evRoutes)
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
		if reorder {
			rows[0].Priority = 500
		}
		w := reviewOrderPut(t, h, p, before, "saved", rows)
		if w.Code != 500 || !strings.Contains(w.Body.String(), "database operation failed") {
			t.Fatal("wrong injected refusal", w.Code, w.Body.String())
		}
		after := h.ladder(t, p, "review-gate")
		if after.OrderMode != "legacy" || *after.EditToken != *before.EditToken || !reflect.DeepEqual(after.Routes, before.Routes) || eventCount(t, p, evRoutes) != n {
			t.Fatal("partial mode or rows after audit failure")
		}
	}
	var reached int
	if err := adminPool.QueryRow(t.Context(), `SELECT last_value FROM review_order_probe`).Scan(&reached); err != nil || reached != 2 {
		t.Fatal("did not reach event injection", err, reached)
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

func TestReviewOrderSavedFallbackPreferencesAndQualification(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		strong, frontier := reviewOrderProfiles(t, tx, p)
		now := time.Now()
		q := WorkQuery{AuthorFamily: "openai"}
		legacy, err := ResolveReviewFor(t.Context(), tx, p, q, now)
		if err != nil {
			return err
		}
		if legacy.Profile == nil || legacy.Profile.ID != frontier.ID || legacy.Trace.OrderMode != "legacy" {
			t.Fatal("legacy frontier/tier fallback changed", legacy)
		}
		if err := saveReviewOrder(t.Context(), tx, p.TenantID, "saved"); err != nil {
			return err
		}
		saved, err := ResolveReviewFor(t.Context(), tx, p, q, now)
		if err != nil {
			return err
		}
		if saved.Profile == nil || saved.Profile.ID != strong.ID || saved.Trace.OrderMode != "saved" || saved.Ladder[0].ProfileID != strong.ID {
			t.Fatal("saved fallback not applied", saved)
		}
		scope, err := modelprefs.SaveScope(t.Context(), tx, p, modelprefs.Scope{Level: "default"})
		if err != nil {
			return err
		}
		var kind string
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM work_kinds WHERE slug='review'`).Scan(&kind); err != nil {
			return err
		}
		if err := modelprefs.PutRow(t.Context(), tx, p, scope, kind, modelprefs.Row{Cells: map[string]modelprefs.Cell{"normal": {Mode: "pinned", ProfileID: frontier.ID}}}); err != nil {
			return err
		}
		preferred, err := ResolveReviewFor(t.Context(), tx, p, q, now)
		if err != nil {
			return err
		}
		if preferred.Profile == nil || preferred.Profile.ID != frontier.ID || !preferred.Ladder[1].Selected || preferred.Trace.OrderMode != "saved" {
			t.Fatal("preference lost qualified precedence", preferred)
		}
		for _, tc := range []struct {
			name, sql, reason string
			query             WorkQuery
		}{
			{"effort", "", "review requires frontier or strong at xhigh", q},
			{"tier", "", "review requires frontier or strong at xhigh", q},
			{"disabled", "", "policy", q},
			{"retired", `INSERT INTO model_profile_retirements(tenant_id,profile_id,reason,retired_by) VALUES($2,$1,'order-test',$3)`, "retired", q},
			{"author", "", "author family", WorkQuery{AuthorFamily: "anthropic"}},
			{"adapter", "", "Codex read-only sandboxing does not isolate inherited MCP tools and startup hooks.", WorkQuery{AuthorFamily: "xai"}},
			{"harness", "", "harness filter", WorkQuery{AuthorFamily: "openai", Harness: "codex"}},
			{"account", `UPDATE agent_accounts SET last_probe_ok=false`, "no approved account with available capacity", q},
			{"residency", "", "not allowed: eu", WorkQuery{AuthorFamily: "openai", TicketResidency: "eu"}},
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
					harness, family, model := "claude", "anthropic", "opus-test"
					if tc.name == "adapter" {
						harness, family, model = "codex", "openai", "gpt-6-sol"
					}
					if err := sub.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier,enabled) VALUES($1,$2,'1',$6,$7,$8,$3,$4,$5) RETURNING id::text`, p.TenantID, "unqualified-"+tc.name, effort, tier, tc.name != "disabled", harness, family, model).Scan(&target); err != nil {
						t.Fatal(err)
					}
					if _, err := sub.Exec(t.Context(), `UPDATE model_role_routes SET profile_id=$2 WHERE profile_id=$1`, frontier.ID, target); err != nil {
						t.Fatal(err)
					}
					if err := modelprefs.PutRow(t.Context(), sub, p, scope, kind, modelprefs.Row{Cells: map[string]modelprefs.Cell{"normal": {Mode: "pinned", ProfileID: target}}}); err != nil {
						t.Fatal(err)
					}
				}
				if tc.sql != "" {
					var err error
					if tc.name == "retired" {
						_, err = sub.Exec(t.Context(), tc.sql, frontier.ID, p.TenantID, p.ID)
					} else if tc.name == "account" {
						_, err = sub.Exec(t.Context(), tc.sql)
					} else {
						_, err = sub.Exec(t.Context(), tc.sql, frontier.ID)
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
					t.Fatal("wrong qualifying refusal", tc.name, candidate)
				}
			})
		}
		security, err := ResolveReviewFor(t.Context(), tx, p, WorkQuery{AuthorFamily: "openai", Area: "security"}, now)
		if err != nil {
			return err
		}
		if !security.OwnerRequired || security.Profile != nil || security.Trace.OrderMode != "" || security.Role != "review-gate-security" || len(security.Trace.Hard) != 1 || security.Trace.Hard[0] != "security_review" {
			t.Fatal("saved order escaped security path", security)
		}
		// Preview caching must retain the same rows+mode even across later writes.
		catalog := &preferencePreviewCatalog{}
		a, err := resolveReviewWithCatalog(t.Context(), tx, p, q, now, catalog)
		if err != nil {
			return err
		}
		if err := saveReviewOrder(t.Context(), tx, p.TenantID, "legacy"); err != nil {
			return err
		}
		b, err := resolveReviewWithCatalog(t.Context(), tx, p, q, now, catalog)
		if err != nil {
			return err
		}
		if a.Trace.OrderMode != "saved" || b.Trace.OrderMode != "saved" || !reflect.DeepEqual(a.Ladder, b.Ladder) {
			t.Fatal("preview paired cached ladder with new mode")
		}
		return nil
	})
}

func TestReviewOrderReadAndResolverSnapshotBarrier(t *testing.T) {
	for _, resolver := range []bool{false, true} {
		t.Run(map[bool]string{false: "GET", true: "resolver"}[resolver], func(t *testing.T) {
			p, h := editorFixture(t)
			var strong, frontier Profile
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error { strong, frontier = reviewOrderProfiles(t, tx, p); return nil }); err != nil {
				t.Fatal(err)
			}
			writer, pid := lockDisplayTable(t)
			if _, err := writer.Exec(t.Context(), `SELECT set_config('aeon.tenant_id',$1,true)`, p.TenantID); err != nil {
				t.Fatal(err)
			}
			if err := saveReviewOrder(t.Context(), writer, p.TenantID, "saved"); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Exec(t.Context(), `UPDATE model_role_routes SET priority=priority+10 WHERE role='review-gate'`); err != nil {
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
			} else if view.OrderMode != "saved" || view.Routes[0].Priority != 11 || *view.EditToken != routeEditToken("review-gate", view.Routes, "saved") || !reflect.DeepEqual(view.ManagedFallbackOrder, []string{strong.ID, frontier.ID}) {
				t.Fatal("GET mixed rows/mode/token", view)
			}
		})
	}
}

func TestReviewOrderFinalRevocationAndConcurrentCAS(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "mode CAS", true: "revoke"}[revoke], func(t *testing.T) {
			p, h := editorFixture(t)
			before := h.ladder(t, p, "review-gate")
			n := eventCount(t, p, evRoutes)
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
			var pid int
			if err := blocker.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			req := startEditorRequest(t, h, p, "PUT", "/api/models/routes?role=review-gate&order_mode=saved", editorJSON(before.Routes), map[string]string{"If-Match": *before.EditToken})
			waitEditorBlocked(t, pid)
			if revoke {
				_, err = blocker.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='member') WHERE principal_id=$1 AND scope_type='workspace'`, p.ID)
			} else {
				err = saveReviewOrder(t.Context(), blocker, p.TenantID, "saved")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			w := finishEditorRequest(t, req)
			if revoke {
				editorError(t, w, 403, "permission denied")
				if h.ladder(t, p, "review-gate").OrderMode != "legacy" {
					t.Fatal("revoked activation committed")
				}
			} else {
				editorError(t, w, 409, "stale_revision")
			}
			if eventCount(t, p, evRoutes) != n || !reflect.DeepEqual(h.ladder(t, p, "review-gate").Routes, before.Routes) {
				t.Fatal("refused mode write changed rows/events")
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
	before := roleRoutes("review-gate", registryRoutes(t, p))
	if len(before) != 6 {
		t.Fatal("historical default fixture lost its rows")
	}
	decode[[]Profile](t, &p, "GET", "/api/models", "", 200)
	after := roleRoutes("review-gate", registryRoutes(t, p))
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("catalog upgrade replaced saved review order: before=%+v after=%+v", before, after)
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
