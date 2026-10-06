// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/jackc/pgx/v5"
)

func TestReadSnapshotReauthorizesAfterCommittedPreparation(t *testing.T) {
	for _, cold := range []bool{true, false} {
		for _, path := range []string{"/api/model-preferences", "/api/models/resolve?role=build&area=backend"} {
			t.Run(path+map[bool]string{true: " cold", false: " additive"}[cold], func(t *testing.T) {
				f := newCatalogFixture(t, false, cold)
				pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(sql string) bool {
					return strings.HasPrefix(strings.ToLower(sql), "begin isolation level repeatable read read only")
				})
				h := f.boundaryHandler(t, pool)
				r := boundaryRequest(ctx, path, nil, "", f.boundaryCookie(t))
				r.Method = "GET"
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- serveBoundary(h, r) }()
				barrier.Wait(t, ctx)
				before := f.catalogReadState(t)
				if err := db.InTenant(dbtest.Seed(ctx), f.d.Admin, f.person.TenantID, func(tx pgx.Tx) error {
					if err := db.LockTenant(ctx, tx, f.person.TenantID); err != nil {
						return err
					}
					_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				barrier.Release()
				out := dbtest.Await(t, ctx, done)
				if out.Code != 403 || !strings.Contains(out.Body.String(), "missing_role_permission") {
					t.Fatalf("wrong final-read revocation %d %s", out.Code, out.Body.String())
				}
				if f.catalogReadState(t) != before {
					t.Fatal("final refusal changed independently authorized setup")
				}
				f.tx(t, func(tx pgx.Tx) error {
					var profiles, seeds int
					if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM model_profiles),(SELECT count(*) FROM events WHERE type='model.registry_seeded')`).Scan(&profiles, &seeds); err != nil {
						return err
					}
					if profiles <= 3 || seeds != 1 {
						t.Fatalf("preparation was not independently committed: %d profiles, %d seeds", profiles, seeds)
					}
					return nil
				})
			})
		}
	}
}

func TestPreferenceSnapshotKeepsCanonicalPersonAndLevelsTogether(t *testing.T) {
	f := newFixture(t)
	linked := testID()
	f.tx(t, func(tx pgx.Tx) error {
		if err := modelprefs.SeedKinds(t.Context(), tx, f.person.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Linked caller')`, f.person.TenantID, linked); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO model_pref_scopes(tenant_id,level,person_id,residency,revision) VALUES($1,'person',$2,'any',1),($1,'person',$3,'local',7)`, f.person.TenantID, f.person.ID, linked)
		return err
	})
	dbtest.BindRole(t, f.d, f.person.TenantID, linked, "member")
	var lookups atomic.Int32
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(sql string) bool {
		// Preparation checks twice. Pause the document's canonical-person read
		// in the final snapshot after its current authority checks passed.
		return strings.HasPrefix(sql, "SELECT (SELECT coalesce(cp.linked_to,cp.id)") && lookups.Add(1) == 3
	})
	h := f.boundaryHandler(t, pool)
	r := boundaryRequest(ctx, "/api/model-preferences", nil, "", f.boundaryCookie(t))
	r.Method = "GET"
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serveBoundary(h, r) }()
	barrier.Wait(t, ctx)
	if err := db.InTenant(dbtest.Seed(ctx), f.d.Admin, f.person.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, f.person.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE principals SET linked_to=$1 WHERE id=$2`, linked, f.person.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE model_pref_scopes SET residency='eu',revision=2 WHERE person_id=$1`, f.person.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	barrier.Release()
	assertDocument := func(out *httptest.ResponseRecorder, person, residency string, revision int64) {
		t.Helper()
		if out.Code != 200 {
			t.Fatalf("snapshot read %d %s", out.Code, out.Body.String())
		}
		var doc struct {
			PersonID string `json:"person_id"`
			Levels   map[string]struct {
				Revision  int64  `json:"revision"`
				Residency string `json:"residency"`
			} `json:"levels"`
			Views map[string]struct {
				Residency struct {
					Value string `json:"value"`
				} `json:"residency"`
			} `json:"views"`
			Can map[string]bool `json:"can"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.PersonID != person || doc.Levels["person"].Revision != revision || doc.Levels["person"].Residency != residency || doc.Views["person"].Residency.Value != residency || !doc.Can["edit_person"] {
			t.Fatalf("mixed canonical/level/view/capability snapshot: %+v", doc)
		}
	}
	assertDocument(dbtest.Await(t, ctx, done), f.person.ID, "any", 1)
	next := boundaryRequest(context.WithoutCancel(ctx), "/api/model-preferences", nil, "", f.boundaryCookie(t))
	next.Method = "GET"
	assertDocument(serveBoundary(f.boundaryHandler(t, f.d.App), next), linked, "local", 7)
}

func TestPlacementReadExactKeyChangesBeforeSetup(t *testing.T) {
	for _, cold := range []bool{true, false} {
		for _, change := range []string{"revoke", "expire", "scope", "creator"} {
			t.Run(change+map[bool]string{true: " cold", false: " additive"}[cold], func(t *testing.T) {
				f := newCatalogFixture(t, false, cold)
				f.tx(t, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['models.read'],created_by_principal_id=$1 WHERE principal_id=$2`, f.person.ID, f.agent.ID)
					return err
				})
				path := "/api/models/resolve?role=build&area=backend&person_id=" + f.person.ID
				pool, barrier, ctx := f.preparationBarrier(t)
				h := f.boundaryHandler(t, pool)
				r := boundaryRequest(ctx, path, nil, f.token, nil)
				r.Method = "GET"
				before := f.catalogReadState(t)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- serveBoundary(h, r) }()
				dbtest.Await(t, ctx, barrier.ready)
				if err := db.InTenant(dbtest.Seed(ctx), f.d.Admin, f.person.TenantID, func(tx pgx.Tx) error {
					if err := db.LockTenant(ctx, tx, f.person.TenantID); err != nil {
						return err
					}
					var err error
					switch change {
					case "revoke":
						_, err = tx.Exec(ctx, `UPDATE agent_keys SET revoked_at=clock_timestamp() WHERE principal_id=$1`, f.agent.ID)
					case "expire":
						_, err = tx.Exec(ctx, `UPDATE agent_keys SET expires_at=clock_timestamp() WHERE principal_id=$1`, f.agent.ID)
					case "scope":
						_, err = tx.Exec(ctx, `UPDATE agent_keys SET scopes=ARRAY['nodes.read'] WHERE principal_id=$1`, f.agent.ID)
					case "creator":
						_, err = tx.Exec(ctx, `UPDATE agent_keys SET created_by_principal_id=NULL WHERE principal_id=$1`, f.agent.ID)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
				barrier.release()
				out := dbtest.Await(t, ctx, done)
				reason := "key scope required: models.read"
				if change == "creator" {
					reason = "person_not_caller"
				}
				if out.Code != 403 || !strings.Contains(out.Body.String(), reason) {
					t.Fatalf("wrong current-key refusal %d %s; want %s", out.Code, out.Body.String(), reason)
				}
				if f.catalogReadState(t) != before {
					t.Fatal("changed initiating key persisted setup")
				}
			})
		}
	}
}

func TestReadPreparationRepeatsCurrentProjectVisibility(t *testing.T) {
	for _, cold := range []bool{true, false} {
		for _, placement := range []bool{false, true} {
			t.Run(map[bool]string{false: "preferences", true: "placement"}[placement]+map[bool]string{true: " cold", false: " additive"}[cold], func(t *testing.T) {
				f := newCatalogFixture(t, false, cold)
				var project, otherProject, role string
				f.tx(t, func(tx pgx.Tx) error {
					for i, dst := range []*string{&project, &otherProject} {
						if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,title,key) SELECT $1,id,'Read project',$2 FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID, []string{"READ-1", "READ-2"}[i]).Scan(dst); err != nil {
							return err
						}
					}
					if _, err := tx.Exec(t.Context(), `UPDATE nodes SET project_id=$1 WHERE id=$2`, project, f.ticket); err != nil {
						return err
					}
					if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'catalog_reader','Catalog reader') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
						return err
					}
					if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'models.read')`, f.person.TenantID, role); err != nil {
						return err
					}
					if _, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$1 WHERE principal_id=$2`, role, f.person.ID); err != nil {
						return err
					}
					_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE key='member'`, f.person.TenantID, f.person.ID, project)
					return err
				})
				pool, barrier, ctx := f.preparationBarrier(t)
				path := "/api/model-preferences?project_id=" + project
				if placement {
					path = "/api/models/resolve?role=build&ticket=" + f.ticket
				}
				r := boundaryRequest(ctx, path, nil, "", f.boundaryCookie(t))
				r.Method = "GET"
				h := f.boundaryHandler(t, pool)
				before := f.catalogReadState(t)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- serveBoundary(h, r) }()
				dbtest.Await(t, ctx, barrier.ready)
				if err := db.InTenant(dbtest.Seed(ctx), f.d.Admin, f.person.TenantID, func(tx pgx.Tx) error {
					if err := authz.LockProjectMutation(ctx, tx, f.person.TenantID); err != nil {
						return err
					}
					if placement {
						_, err := tx.Exec(ctx, `UPDATE nodes SET project_id=$1 WHERE id=$2`, otherProject, f.ticket)
						return err
					}
					_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1 AND scope_type='project'`, f.person.ID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				barrier.release()
				out := dbtest.Await(t, ctx, done)
				if placement {
					// The moved target is hidden by freshly derived RLS visibility.
					if out.Code != 404 || !strings.Contains(out.Body.String(), "not found") {
						t.Fatalf("wrong moved-target refusal %d %s", out.Code, out.Body.String())
					}
				} else if out.Code != 403 || !strings.Contains(out.Body.String(), "missing_role_permission") {
					t.Fatalf("wrong project revocation %d %s", out.Code, out.Body.String())
				}
				if !placement {
					f.tx(t, func(tx pgx.Tx) error {
						if err := authz.RequireTx(ctx, tx, f.person, "models.read", authz.Scope{}); err != nil {
							return err
						}
						if err := authz.RequireTx(ctx, tx, f.person, "nodes.read", authz.Scope{ProjectID: project}); !errors.Is(err, authz.ErrForbidden) {
							t.Fatalf("wrong permission removed: %v", err)
						}
						return nil
					})
				}
				if f.catalogReadState(t) != before {
					t.Fatal("lost project visibility persisted setup")
				}
			})
		}
	}
}

func TestReadPreparationHoldsFenceAgainstRevocation(t *testing.T) {
	for _, cold := range []bool{true, false} {
		for _, path := range []string{"/api/model-preferences", "/api/models/resolve?role=build&area=backend"} {
			t.Run(path+map[bool]string{true: " cold", false: " additive"}[cold], func(t *testing.T) {
				f := newCatalogFixture(t, false, cold)
				pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(sql string) bool { return strings.Contains(sql, "INSERT INTO model_profiles") })
				h := f.boundaryHandler(t, pool)
				r := boundaryRequest(ctx, path, nil, "", f.boundaryCookie(t))
				r.Method = "GET"
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- serveBoundary(h, r) }()
				holder := barrier.Wait(t, ctx)
				revoked := make(chan error, 1)
				finished := make(chan struct{})
				go func() {
					err := db.InTenant(dbtest.Seed(ctx), f.d.Admin, f.person.TenantID, func(tx pgx.Tx) error {
						if err := db.LockTenant(ctx, tx, f.person.TenantID); err != nil {
							return err
						}
						_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
						return err
					})
					revoked <- err
					close(finished)
				}()
				if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, holder, finished); lock == "" {
					t.Fatal("revoker crossed preparation fence")
				}
				barrier.Release()
				if err := dbtest.Await(t, ctx, revoked); err != nil {
					t.Fatal(err)
				}
				out := dbtest.Await(t, ctx, done)
				// A read snapshot can start before the queued revoker acquires the
				// fence. Either serial order is legitimate; the next read must deny.
				if out.Code != 200 && !(out.Code == 403 && strings.Contains(out.Body.String(), "missing_role_permission")) {
					t.Fatalf("unexpected inverse read %d %s", out.Code, out.Body.String())
				}
				f.tx(t, func(tx pgx.Tx) error {
					var profiles, seeds int
					if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM model_profiles),(SELECT count(*) FROM events WHERE type='model.registry_seeded')`).Scan(&profiles, &seeds); err != nil {
						return err
					}
					if profiles <= 3 || seeds != 1 {
						t.Fatalf("authorized setup lost: %d profiles, %d seeds", profiles, seeds)
					}
					return nil
				})
				state := f.catalogReadState(t)
				next := boundaryRequest(ctx, path, nil, "", f.boundaryCookie(t))
				next.Method = "GET"
				out = serveBoundary(f.boundaryHandler(t, f.d.App), next)
				if out.Code != 403 || !strings.Contains(out.Body.String(), "missing_role_permission") {
					t.Fatalf("read after revocation %d %s", out.Code, out.Body.String())
				}
				if state != f.catalogReadState(t) {
					t.Fatal("denied retry changed authorized setup")
				}
			})
		}
	}
}

func (f *fixture) catalogReadState(t *testing.T) string {
	t.Helper()
	var state string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT jsonb_build_object(
		 'profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM model_profiles p),
		 'routes',(SELECT jsonb_agg(to_jsonb(r) ORDER BY role,priority) FROM model_role_routes r),
		 'seeds',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM events e WHERE type='model.registry_seeded'),
		 'counter',(SELECT to_jsonb(c) FROM event_counters c))::text`).Scan(&state)
	})
	return state
}

func TestPreferenceAndPlacementReadInitializeThroughMiddleware(t *testing.T) {
	for _, cold := range []bool{true, false} {
		for _, path := range []string{"/api/model-preferences", "/api/models/resolve?role=build&ticket="} {
			t.Run(path+map[bool]string{true: " cold", false: " additive"}[cold], func(t *testing.T) {
				f := newCatalogFixture(t, false, cold)
				f.tx(t, func(tx pgx.Tx) error { return modelprefs.SeedKinds(t.Context(), tx, f.person.TenantID) })
				dbtest.BindRole(t, f.d, f.person.TenantID, f.person.ID, "member") // models.read, no models.manage
				if strings.HasSuffix(path, "ticket=") {
					path += f.ticket
				}
				r := boundaryRequest(t.Context(), path, nil, "", f.boundaryCookie(t))
				r.Method = "GET"
				out := serveBoundary(f.boundaryHandler(t, f.d.App), r)
				if out.Code != 200 {
					t.Fatalf("read initializer %d %s", out.Code, out.Body.String())
				}
				var body map[string]json.RawMessage
				if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if path == "/api/model-preferences" {
					var kinds []modelprefs.Kind
					if err := json.Unmarshal(body["kinds"], &kinds); err != nil || len(kinds) == 0 {
						t.Fatalf("missing preference kinds: %s", body["kinds"])
					}
					var person string
					if err := json.Unmarshal(body["person_id"], &person); err != nil || person != f.person.ID {
						t.Fatalf("wrong preference caller %s", body["person_id"])
					}
				} else if len(body["preference"]) == 0 {
					t.Fatal("placement read returned the legacy resolver response")
				}
				f.tx(t, func(tx pgx.Tx) error {
					var profiles, seeds int
					if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM model_profiles),(SELECT count(*) FROM events WHERE type='model.registry_seeded')`).Scan(&profiles, &seeds); err != nil {
						return err
					}
					if profiles <= 3 || seeds != 1 {
						t.Fatalf("setup did not commit once: profiles=%d seeds=%d", profiles, seeds)
					}
					return nil
				})
			})
		}
	}
}

func TestPreferenceAndPlacementReadRevocationBeforeSetup(t *testing.T) {
	for _, cold := range []bool{true, false} {
		for _, path := range []string{"/api/model-preferences", "/api/models/resolve?role=build&ticket="} {
			t.Run(path+map[bool]string{true: " cold", false: " additive"}[cold], func(t *testing.T) {
				f := newCatalogFixture(t, false, cold)
				if strings.HasSuffix(path, "ticket=") {
					path += f.ticket
				}
				before := f.catalogReadState(t)
				pool, barrier, ctx := f.preparationBarrier(t)
				r := boundaryRequest(ctx, path, nil, "", f.boundaryCookie(t))
				r.Method = "GET"
				h := f.boundaryHandler(t, pool)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- serveBoundary(h, r) }()
				dbtest.Await(t, ctx, barrier.ready)
				if err := db.InTenant(dbtest.Seed(ctx), f.d.Admin, f.person.TenantID, func(tx pgx.Tx) error {
					if err := db.LockTenant(ctx, tx, f.person.TenantID); err != nil {
						return err
					}
					_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				barrier.release()
				out := dbtest.Await(t, ctx, done)
				if out.Code != 403 || !strings.Contains(out.Body.String(), "missing_role_permission") {
					t.Fatalf("wrong revoked read %d %s", out.Code, out.Body.String())
				}
				if f.catalogReadState(t) != before {
					t.Fatal("revoked read persisted catalog setup")
				}
			})
		}
	}
}

func TestPlacementReadKeyPreparesWithoutManagementScope(t *testing.T) {
	for _, cold := range []bool{true, false} {
		t.Run(map[bool]string{true: "cold", false: "additive"}[cold], func(t *testing.T) {
			f := newCatalogFixture(t, false, cold)
			f.tx(t, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['models.read'],created_by_principal_id=$1 WHERE principal_id=$2`, f.person.ID, f.agent.ID)
				return err
			})
			r := boundaryRequest(t.Context(), "/api/models/resolve?role=build&area=backend&person_id="+f.person.ID, nil, f.token, nil)
			r.Method = "GET"
			out := serveBoundary(f.boundaryHandler(t, f.d.App), r)
			if out.Code != 200 {
				t.Fatalf("read-only key could not prepare placement %d %s", out.Code, out.Body.String())
			}
			var body struct {
				Preference struct {
					PersonID string `json:"person_id"`
				} `json:"preference"`
			}
			if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Preference.PersonID != f.person.ID {
				t.Fatal("key creator was not captured in placement evidence")
			}
			before := f.catalogReadState(t)
			// Keep the existing middleware ceiling: preference GET is not on the
			// agent-key allowlist, even though placement resolution is.
			r = boundaryRequest(t.Context(), "/api/model-preferences", nil, f.token, nil)
			r.Method = "GET"
			out = serveBoundary(f.boundaryHandler(t, f.d.App), r)
			if out.Code != 403 || !strings.Contains(out.Body.String(), "agent key scope required") {
				t.Fatalf("preference read broadened the key ceiling %d %s", out.Code, out.Body.String())
			}
			if before != f.catalogReadState(t) {
				t.Fatal("blocked preference key changed setup")
			}
		})
	}
}

func TestReadInitializerSeedFailureRollsBackSetup(t *testing.T) {
	for _, cold := range []bool{true, false} {
		for _, path := range []string{"/api/model-preferences", "/api/models/resolve?role=build&area=backend"} {
			t.Run(path+map[bool]string{true: " cold", false: " additive"}[cold], func(t *testing.T) {
				f := newCatalogFixture(t, false, cold)
				before := f.catalogReadState(t)
				f.armFailureProbe(t)
				if _, err := f.d.Admin.Exec(t.Context(), `CREATE FUNCTION reject_read_seed() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='model.registry_seeded' THEN PERFORM nextval('aeon633_failure_hits'); RAISE EXCEPTION 'read seed failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_read_seed BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_read_seed()`); err != nil {
					t.Fatal(err)
				}
				r := boundaryRequest(t.Context(), path, nil, "", f.boundaryCookie(t))
				r.Method = "GET"
				out := serveBoundary(f.boundaryHandler(t, f.d.App), r)
				if out.Code != 500 || !strings.Contains(out.Body.String(), "database operation failed") {
					t.Fatalf("seed failure reported success or wrong refusal %d %s", out.Code, out.Body.String())
				}
				f.assertFailureProbe(t)
				if before != f.catalogReadState(t) {
					t.Fatal("failed seed retained profiles/routes/events/counter")
				}
			})
		}
	}
}
