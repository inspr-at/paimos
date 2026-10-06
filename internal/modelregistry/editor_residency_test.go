// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/jackc/pgx/v5"
)

func TestEditorModelLocksAndAdvisoryResidency(t *testing.T) {
	p, h := editorFixture(t)
	reader := addPrincipal(t, p.TenantID, "person", "Narrower", []string{"member"})
	doc := h.prefs(t, p, "")
	kind := editorKind(t, doc, "backend")
	profiles := editorDecode[[]Profile](t, h.call(t, p, "GET", "/api/models", "", nil))
	a, b := modelprefs.Cell{Mode: "pinned", ProfileID: profiles[0].ID}, modelprefs.Cell{Mode: "pinned", ProfileID: profiles[1].ID}
	base := "/api/model-preferences/levels/default"
	headers := prefHeaders(p.ID)
	saved := editorDecode[preferenceWriteResult](t, h.call(t, p, "PUT", base+"/rows/"+kind, prefRowPayload(0, false, a, b), headers))
	locked := editorDecode[preferenceWriteResult](t, h.call(t, p, "PUT", base+"/rows/"+kind, fmt.Sprintf(`{"revision":%d,"locked":true}`, saved.Revision), headers))
	r := hasProjectedRow(locked.Level, kind)
	if r == nil || !r.Locked || r.Normal != a || r.Complex != b {
		t.Fatal("copy-on-lock did not snapshot both buckets")
	}
	personRow := "/api/model-preferences/levels/person/rows/" + kind
	before := preferenceStorage(t, p)
	editorError(t, h.call(t, reader, "PUT", personRow, prefRowPayload(0, false, modelprefs.Cell{Mode: "auto"}, modelprefs.Cell{Mode: "auto"}), prefHeaders(reader.ID)), 422, "locked_above")
	if preferenceStorage(t, p) != before {
		t.Fatal("locked refusal changed data")
	}
	editorDecode[preferenceWriteResult](t, h.call(t, p, "PUT", base, fmt.Sprintf(`{"revision":%d,"residency":"eu","residency_locked":true,"prefs_locked":true}`, locked.Revision), headers))
	loose := editorDecode[preferenceWriteResult](t, h.call(t, reader, "PUT", "/api/model-preferences/levels/person", `{"revision":0,"residency":"any","residency_locked":false,"prefs_locked":false}`, prefHeaders(reader.ID)))
	if loose.Residency.Value != "any" || !loose.Residency.LoosenedLock {
		t.Fatal("advisory residency became enforced refusal")
	}
	doc = h.prefs(t, reader, "")
	if doc.ResidencyLockMode != "warn" || !doc.Views["person"].Residency.LoosenedLock {
		t.Fatal("warning absent from GET")
	}
	project := editorProject(t, p, "LOCK-1")
	editorError(t, h.call(t, p, "PUT", "/api/model-preferences/levels/project/rows/"+kind+"?project_id="+project, prefRowPayload(0, true, a, b), headers), 422, "locked_above")
	// Project locks themselves remain prohibited after removing the broader lock.
	editorDecode[preferenceWriteResult](t, h.call(t, p, "DELETE", base+"?revision=3", "", headers))
	editorError(t, h.call(t, p, "PUT", "/api/model-preferences/levels/project/rows/"+kind+"?project_id="+project, prefRowPayload(0, true, a, b), headers), 422, "lock_at_project")
	editorError(t, h.call(t, reader, "PUT", personRow, prefRowPayload(1, false, modelprefs.Cell{Mode: "pinned", ProfileID: "00000000-0000-0000-0000-000000000001"}, b), prefHeaders(reader.ID)), 422, "unknown_profile")
}

func TestEditorScalarUndoKeepsStricterRunStampsAndSessionActor(t *testing.T) {
	p, h := editorFixture(t)
	alias := addPrincipal(t, p.TenantID, "person", "Queued alias", []string{"member"})
	other := addPrincipal(t, p.TenantID, "person", "Other person", []string{"member"})
	agent := addPrincipal(t, p.TenantID, "agent", "Runner", []string{"admin"})
	profiles := editorDecode[[]Profile](t, h.call(t, p, "GET", "/api/models", "", nil))
	profile := profiles[0]
	ids := map[string]string{}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var order, account string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'RESTAMP-1','Work' FROM node_kinds WHERE slug='work_order' RETURNING id::text`, p.TenantID).Scan(&order); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES($1,$2,$3)`, p.TenantID, order, p.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label) VALUES($1,'editor-cloud',$2,'editor-daemon',$3,'Cloud') RETURNING id::text`, p.TenantID, profile.Harness, agent.ID).Scan(&account); err != nil {
			return err
		}
		for _, status := range []string{"queued", "starting", "running", "waiting", "completed", "other"} {
			starter, state := alias.ID, status
			if status == "other" {
				starter, state = other.ID, "queued"
			}
			var id string
			if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,status,model_profile_id,account_id,prefs_person_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id::text`, p.TenantID, order, agent.ID, state, profile.ID, account, starter).Scan(&id); err != nil {
				return err
			}
			ids[status] = id
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	linkEditorPerson(t, p, alias.ID, p.ID)
	path := "/api/model-preferences/levels/person"
	headers := prefHeaders(p.ID)
	tightened := editorDecode[preferenceWriteResult](t, h.call(t, alias, "PUT", path, `{"revision":0,"residency":"local","residency_locked":false,"prefs_locked":false}`, headers))
	if len(tightened.RunningOutside) != 2 {
		t.Fatal("running_outside not honest", tightened.RunningOutside)
	}
	for _, id := range []string{ids["starting"], ids["running"]} {
		found := false
		for _, outside := range tightened.RunningOutside {
			found = found || outside == id
		}
		if !found {
			t.Fatal("missed running-outside account")
		}
	}
	var stamps string
	readStamps := func() string {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT jsonb_object_agg(id::text,residency)::text FROM agent_runs`).Scan(&stamps)
		}); err != nil {
			t.Fatal(err)
		}
		return stamps
	}
	before := readStamps()
	count := eventCount(t, p, "run.residency_restamped")
	if count != 4 {
		t.Fatal("wrong active run count", count)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		for state, id := range ids {
			var stamp *string
			if err := tx.QueryRow(t.Context(), `SELECT residency FROM agent_runs WHERE id=$1`, id).Scan(&stamp); err != nil {
				return err
			}
			if state == "completed" || state == "other" {
				if stamp != nil {
					t.Fatal("terminal/other run restamped")
				}
			} else if stamp == nil || *stamp != "local" {
				t.Fatal("active pre-link run missed")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Absent scope compensation is scalar PUT null/false/false, never DELETE.
	undo := editorDecode[preferenceWriteResult](t, h.call(t, alias, "PUT", path, fmt.Sprintf(`{"revision":%d,"residency":null,"residency_locked":false,"prefs_locked":false}`, tightened.Revision), headers))
	if undo.Level.Residency != nil || undo.Level.PrefsLocked || undo.Level.ResidencyLocked || undo.Revision != 2 || readStamps() != before || eventCount(t, p, "run.residency_restamped") != count {
		t.Fatal("scalar Undo reduced a run stamp or removed scope")
	}
	_, _, actor := mutationProjection(t, p)
	if actor != alias.ID || undo.PersonID == nil || *undo.PersonID != p.ID {
		t.Fatal("canonical target changed audit actor")
	}
	// A failed final preference event must also roll back an earlier collected
	// restamp and its event, even though several old runs retain stricter floors.
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,status,model_profile_id,account_id,prefs_person_id)
		 SELECT tenant_id,work_order_id,agent_principal_id,'queued',model_profile_id,account_id,prefs_person_id FROM agent_runs WHERE id=$1`, ids["queued"])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(t.Context(), `CREATE SEQUENCE editor_residency_probe; CREATE FUNCTION editor_residency_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	 IF NEW.type='model.preferences_changed' THEN
	  IF (SELECT count(*) FROM events WHERE type='run.residency_restamped')<>5 THEN RAISE EXCEPTION 'missing earlier restamp event'; END IF;
	  PERFORM nextval('editor_residency_probe'); RAISE EXCEPTION 'injected preference event failure after restamp';
	 END IF; RETURN NEW; END $$;
	 CREATE TRIGGER editor_residency_fail BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION editor_residency_fail()`); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(t.Context(), `GRANT USAGE ON SEQUENCE editor_residency_probe TO `+pgx.Identifier{testDB.Role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(context.Background(), `DROP TRIGGER editor_residency_fail ON events; DROP FUNCTION editor_residency_fail(); DROP SEQUENCE editor_residency_probe`); err != nil {
			t.Error(err)
		}
	})
	beforeFailure := preferenceStorage(t, p)
	w := h.call(t, alias, "PUT", path, `{"revision":2,"residency":"eu","residency_locked":false,"prefs_locked":false}`, headers)
	if w.Code != 500 || preferenceStorage(t, p) != beforeFailure {
		t.Fatal("preference event failure left run/scalar/event changes", w.Code)
	}
	var called bool
	if err := adminPool.QueryRow(t.Context(), `SELECT is_called FROM editor_residency_probe`).Scan(&called); err != nil || !called {
		t.Fatal("injection did not observe the new restamp event before failure", err)
	}
}
