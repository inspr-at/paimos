// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/principallink"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type identityMutation struct{ method, path, body string }

func identityMutationRequest(action, kind string, revision int64) identityMutation {
	level := "/api/model-preferences/levels/person"
	row := level + "/rows/" + kind
	switch action {
	case "row-save", "row-undo":
		return identityMutation{"PUT", row, prefRowPayload(revision, false, modelprefs.Cell{Mode: "auto"}, modelprefs.Cell{Mode: "auto"})}
	case "row-reset":
		return identityMutation{"DELETE", fmt.Sprintf("%s?revision=%d", row, revision), ""}
	case "scalar-undo":
		return identityMutation{"PUT", level, fmt.Sprintf(`{"revision":%d,"residency":null}`, revision)}
	case "scalar-save":
		return identityMutation{"PUT", level, fmt.Sprintf(`{"revision":%d,"residency":"local"}`, revision)}
	default:
		return identityMutation{"DELETE", fmt.Sprintf("%s?revision=%d", level, revision), ""}
	}
}

func identityRaceFixture(t *testing.T, change string, revision int64) (tenant.Principal, *editorHTTP, tenant.Principal, []tenant.Principal, string, string, string) {
	t.Helper()
	p, h := editorFixture(t)
	people := []tenant.Principal{
		addPrincipal(t, p.TenantID, "person", "Identity session", []string{"member"}),
		addPrincipal(t, p.TenantID, "person", "First canonical", []string{"member"}),
		addPrincipal(t, p.TenantID, "person", "Second canonical", []string{"member"}),
	}
	kind := editorKind(t, h.prefs(t, people[0], ""), "backend")
	for _, person := range people {
		for rev := int64(0); rev < revision; rev++ {
			result := editorDecode[preferenceWriteResult](t, h.call(t, person, "PUT", "/api/model-preferences/levels/person/rows/"+kind, prefRowPayload(rev, false, modelprefs.Cell{Mode: "auto"}, modelprefs.Cell{Mode: "auto"}), prefHeaders(person.ID)))
			if result.Revision != rev+1 {
				t.Fatal("fixture revision mismatch")
			}
		}
	}
	agent := addPrincipal(t, p.TenantID, "agent", "Identity runner", []string{"admin"})
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var order string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'IDENTITY-1','Identity work' FROM node_kinds WHERE slug='work_order' RETURNING id::text`, p.TenantID).Scan(&order); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES($1,$2,$3)`, p.TenantID, order, p.ID); err != nil {
			return err
		}
		for _, who := range people {
			if _, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,status,prefs_person_id) VALUES($1,$2,$3,'queued',$4)`, p.TenantID, order, agent.ID, who.ID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	old, next := people[0].ID, people[1].ID
	if change != "link" {
		linkEditorPerson(t, p, people[0].ID, people[1].ID)
		old = people[1].ID
		next = ""
		if change == "relink" {
			next = people[2].ID
		}
	}
	doc := h.prefs(t, people[0], "")
	if doc.PersonID == nil || *doc.PersonID != old || doc.Levels["person"].Revision != revision {
		t.Fatal("race fixture identity/revision not captured")
	}
	return p, h, people[0], people, kind, old, next
}
func changeIdentityTx(t *testing.T, tx pgx.Tx, p tenant.Principal, session, next string, relink bool) {
	t.Helper()
	if relink {
		if _, err := principallink.LinkTx(t.Context(), tx, p.TenantID, session, "", p.ID, "principal.linked", "principal.unlinked"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := principallink.LinkTx(t.Context(), tx, p.TenantID, session, next, p.ID, "principal.linked", "principal.unlinked"); err != nil {
		t.Fatal(err)
	}
}

// Assert the actual tenant-fence statement, so a principal/table wait cannot
// masquerade as the required access/identity serialization.
func identityFenceWaiter(t *testing.T, blocker int) int {
	t.Helper()
	var pid int
	var query string
	if err := adminPool.QueryRow(t.Context(), `SELECT pid,query FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) LIMIT 1`, blocker).Scan(&pid, &query); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "FROM tenants") || !strings.Contains(query, "FOR NO KEY UPDATE") {
		t.Fatalf("expected tenant-fence wait, got %s", query)
	}
	return pid
}

func identityStateTx(t *testing.T, tx pgx.Tx) string {
	t.Helper()
	var state string
	if err := tx.QueryRow(t.Context(), preferenceStorageSQL).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// Compensation uses a confirmed mutation and equal revisions at every target.
func identityCompensationFixture(t *testing.T, h *editorHTTP, session tenant.Principal, people []tenant.Principal, change, action, kind string, revision int64) int64 {
	t.Helper()
	if !strings.HasSuffix(action, "undo") {
		return revision
	}
	forward := "row-reset"
	if action == "scalar-undo" {
		forward = "scalar-save"
	}
	for _, who := range people {
		if change != "link" && who.ID == session.ID {
			continue
		}
		q := identityMutationRequest(forward, kind, revision)
		caller := who
		if change == "link" && who.ID == session.ID || change != "link" && who.ID == people[1].ID {
			caller = session // Save as the same linked session that later invokes Undo.
		}
		result := editorDecode[preferenceWriteResult](t, h.call(t, caller, q.method, q.path, q.body, prefHeaders(who.ID)))
		if result.PersonID == nil || *result.PersonID != who.ID {
			t.Fatal("compensation confirmation targeted wrong person")
		}
		if result.Revision != revision+1 {
			t.Fatal("compensation fixture revision mismatch")
		}
	}
	return revision + 1
}

func TestEditorIdentityChangesBeforeMutationFence(t *testing.T) {
	for _, change := range []string{"link", "unlink", "relink"} {
		for _, revision := range []int64{0, 1} {
			for _, action := range []string{"row-save", "row-reset", "row-undo", "scalar-save", "scalar-undo", "level-reset"} {
				t.Run(fmt.Sprintf("%s/revision-%d/%s", change, revision, action), func(t *testing.T) {
					revision := revision
					p, h, session, people, kind, old, next := identityRaceFixture(t, change, revision)
					revision = identityCompensationFixture(t, h, session, people, change, action, kind, revision)
					blocker, err := adminPool.Begin(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					defer blocker.Rollback(context.Background())
					if _, err = blocker.Exec(t.Context(), `SELECT set_config('aeon.tenant_id',$1,true)`, p.TenantID); err != nil {
						t.Fatal(err)
					}
					if err = db.LockTenant(t.Context(), blocker, p.TenantID); err != nil {
						t.Fatal(err)
					}
					var pid int
					if err = blocker.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
						t.Fatal(err)
					}
					q := identityMutationRequest(action, kind, revision)
					done := startEditorRequest(t, h, session, q.method, q.path, q.body, prefHeaders(old))
					// Middleware and decoding completed; the final write is observed waiting
					// on the identity-change transaction's tenant fence before it changes identity.
					waitEditorBlocked(t, pid)
					identityFenceWaiter(t, pid)
					changeIdentityTx(t, blocker, p, session.ID, next, change == "relink")
					expected := identityStateTx(t, blocker)
					if err = blocker.Commit(t.Context()); err != nil {
						t.Fatal(err)
					}
					response := finishEditorRequest(t, done)
					editorError(t, response, 409, "preference_person_changed")
					var refusal struct {
						Code string `json:"code"`
					}
					if err := json.Unmarshal(response.Body.Bytes(), &refusal); err != nil {
						t.Fatal(err)
					}
					if refusal.Code != "preference_person_changed" {
						t.Fatal("identity conflict code missing or wrong")
					}
					if actual := preferenceStorage(t, p); actual != expected {
						t.Fatal("refused captured-person write changed scopes, rows, cells, runs, events or counter")
					}
				})
			}
		}
	}
}

func personSliceState(t *testing.T, p tenant.Principal, person string) string {
	t.Helper()
	var state string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT jsonb_build_object('scope',to_jsonb(s),'rows',(SELECT jsonb_agg(to_jsonb(r) ORDER BY kind_id) FROM model_pref_rows r WHERE r.scope_id=s.id),'cells',(SELECT jsonb_agg(to_jsonb(c) ORDER BY kind_id,bucket) FROM model_pref_cells c WHERE c.scope_id=s.id))::text FROM model_pref_scopes s WHERE s.person_id=$1`, person).Scan(&state)
	}); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestEditorIdentityChangeWaitsBehindFencedMutation(t *testing.T) {
	for _, change := range []string{"link", "unlink", "relink"} {
		for _, action := range []string{"row-save", "row-reset", "row-undo", "scalar-save", "scalar-undo", "level-reset"} {
			t.Run(change+"/"+action, func(t *testing.T) {
				p, h, session, people, kind, old, next := identityRaceFixture(t, change, 1)
				revision := identityCompensationFixture(t, h, session, people, change, action, kind, 1)
				otherStates := map[string]string{}
				for _, who := range people {
					if who.ID != old {
						otherStates[who.ID] = personSliceState(t, p, who.ID)
					}
				}
				beforeEvents := eventCount(t, p, "model.preferences_changed")
				blocker, err := adminPool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback(context.Background())
				if _, err = blocker.Exec(t.Context(), `LOCK TABLE model_pref_scopes IN ACCESS EXCLUSIVE MODE`); err != nil {
					t.Fatal(err)
				}
				var pid int
				if err = blocker.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
					t.Fatal(err)
				}
				q := identityMutationRequest(action, kind, revision)
				done := startEditorRequest(t, h, session, q.method, q.path, q.body, prefHeaders(old))
				waitEditorBlocked(t, pid)
				var writer int
				if err = adminPool.QueryRow(t.Context(), `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) LIMIT 1`, pid).Scan(&writer); err != nil {
					t.Fatal(err)
				}
				changed := make(chan error, 1)
				go func() {
					changed <- db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
						if err := db.LockTenant(t.Context(), tx, p.TenantID); err != nil {
							return err
						}
						if change == "relink" {
							if _, err := principallink.LinkTx(t.Context(), tx, p.TenantID, session.ID, "", p.ID, "principal.linked", "principal.unlinked"); err != nil {
								return err
							}
						}
						_, err := principallink.LinkTx(t.Context(), tx, p.TenantID, session.ID, next, p.ID, "principal.linked", "principal.unlinked")
						return err
					})
				}()
				waitEditorBlocked(t, writer)
				identityFenceWaiter(t, writer)
				if err = blocker.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				result := editorDecode[preferenceWriteResult](t, finishEditorRequest(t, done))
				select {
				case err = <-changed:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("identity change remained blocked")
				}
				if result.PersonID == nil || *result.PersonID != old || result.Revision != revision+1 {
					t.Fatal("fenced mutation did not confirm captured identity/revision")
				}
				if eventCount(t, p, "model.preferences_changed") != beforeEvents+1 {
					t.Fatal("fenced write did not emit exactly one preference event")
				}
				before, after, actor := mutationProjection(t, p)
				if actor != session.ID || before.Revision != revision || after.Revision != revision+1 {
					t.Fatal("fenced write audit actor/revision mismatch")
				}
				for person, state := range otherStates {
					if personSliceState(t, p, person) != state {
						t.Fatal("fenced write touched a different person's slice")
					}
				}
				var stored, canonical string
				if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT s.person_id::text,coalesce(pr.linked_to,pr.id)::text FROM model_pref_scopes s JOIN events e ON e.after->>'scope'=s.id::text JOIN principals pr ON pr.id=$1 WHERE e.type='model.preferences_changed' ORDER BY e.id DESC LIMIT 1`, session.ID).Scan(&stored, &canonical)
				}); err != nil {
					t.Fatal(err)
				}
				wantNext := next
				if wantNext == "" {
					wantNext = session.ID
				}
				if stored != old || canonical != wantNext {
					t.Fatal("write/link ordering affected wrong identity")
				}
			})
		}
	}
}
