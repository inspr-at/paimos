// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func storedKindRows(t *testing.T, p tenant.Principal, kind string) string {
	t.Helper()
	var out string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT jsonb_build_object('rows',(SELECT jsonb_agg(to_jsonb(r) ORDER BY scope_id) FROM model_pref_rows r WHERE kind_id=$1),'cells',(SELECT jsonb_agg(to_jsonb(c) ORDER BY scope_id,bucket) FROM model_pref_cells c WHERE kind_id=$1))::text`, kind).Scan(&out)
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
func mutationProjection(t *testing.T, p tenant.Principal) (preferenceLevel, preferenceLevel, string) {
	t.Helper()
	var before, after []byte
	var actor string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT before->'value',after->'after',actor_principal_id::text FROM events WHERE type='model.preferences_changed' ORDER BY id DESC LIMIT 1`).Scan(&before, &after, &actor)
	}); err != nil {
		t.Fatal(err)
	}
	var a, b preferenceLevel
	if err := json.Unmarshal(before, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &b); err != nil {
		t.Fatal(err)
	}
	return a, b, actor
}
func hasProjectedRow(level preferenceLevel, id string) *preferenceRow {
	for _, r := range level.Rows {
		if r.KindID == id {
			return &r
		}
	}
	return nil
}

func TestEditorRowResetUndoPreservesArchivedAndSiblingStorage(t *testing.T) {
	for _, level := range []string{"default", "person", "project"} {
		t.Run(level, func(t *testing.T) {
			p, h := editorFixture(t)
			project := ""
			if level == "project" {
				if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'PRESERVE-1','Project' FROM node_kinds WHERE slug='project' RETURNING id::text`, p.TenantID).Scan(&project)
				}); err != nil {
					t.Fatal(err)
				}
			}
			q := ""
			if project != "" {
				q = "?project_id=" + project
			}
			doc := h.prefs(t, p, project)
			target, sibling := editorKind(t, doc, "backend"), editorKind(t, doc, "other")
			created := h.call(t, p, "POST", "/api/work-kinds", editorJSON(map[string]string{"label": "Archived special", "hint": "Keep exact settings", "project_id": project}), nil)
			if created.Code != 201 {
				t.Fatal(created.Code, created.Body.String())
			}
			var archived workKind
			if err := json.Unmarshal(created.Body.Bytes(), &archived); err != nil {
				t.Fatal(err)
			}
			profiles := editorDecode[[]Profile](t, h.call(t, p, "GET", "/api/models", "", nil))
			normal := modelprefs.Cell{Mode: "pinned", ProfileID: profiles[0].ID}
			complex := modelprefs.Cell{Mode: "pinned", ProfileID: profiles[1].ID}
			locked := level != "project"
			base := "/api/model-preferences/levels/" + level
			headers := prefHeaders(p.ID)
			var rev int64
			for _, id := range []string{target, sibling, archived.ID} {
				result := editorDecode[preferenceWriteResult](t, h.call(t, p, "PUT", base+"/rows/"+id+q, prefRowPayload(rev, locked, normal, complex), headers))
				rev = result.Revision
			}
			editorDecode[workKind](t, h.call(t, p, "DELETE", "/api/work-kinds/"+archived.ID, "", nil))
			doc = h.prefs(t, p, project)
			for _, k := range doc.Kinds {
				if k.ID == archived.ID {
					t.Fatal("GET exposed archived kind")
				}
			}
			keepArchived, keepSibling := storedKindRows(t, p, archived.ID), storedKindRows(t, p, sibling)
			reset := editorDecode[preferenceWriteResult](t, h.call(t, p, "DELETE", fmt.Sprintf("%s/rows/%s%s%crevision=%d", base, target, q, map[bool]byte{true: '&', false: '?'}[q != ""], rev), "", headers))
			if hasProjectedRow(reset.Level, target) != nil {
				t.Fatal("reset retained row")
			}
			var physical map[string]any
			if err := json.Unmarshal([]byte(storedKindRows(t, p, target)), &physical); err != nil {
				t.Fatal(err)
			}
			if physical["rows"] != nil || physical["cells"] != nil {
				t.Fatal("row Reset did not delete physical row and both cascading cells")
			}
			before, after, actor := mutationProjection(t, p)
			old := hasProjectedRow(before, target)
			if old == nil || old.Normal != normal || old.Complex != complex || old.Locked != locked || hasProjectedRow(after, target) != nil || actor != p.ID {
				t.Fatal("reset audit missing exact row/absence")
			}
			if hasProjectedRow(before, archived.ID) != nil || hasProjectedRow(after, archived.ID) != nil {
				t.Fatal("visible projection claimed archived snapshot")
			}
			if storedKindRows(t, p, archived.ID) != keepArchived || storedKindRows(t, p, sibling) != keepSibling {
				t.Fatal("Reset destroyed unrelated storage")
			}
			undo := editorDecode[preferenceWriteResult](t, h.call(t, p, "PUT", base+"/rows/"+target+q, prefRowPayload(reset.Revision, locked, normal, complex), headers))
			rev = undo.Revision
			restored := hasProjectedRow(undo.Level, target)
			if restored == nil || restored.Normal != normal || restored.Complex != complex || restored.Locked != locked {
				t.Fatal("Undo did not restore exact two buckets/lock")
			}
			if storedKindRows(t, p, archived.ID) != keepArchived || storedKindRows(t, p, sibling) != keepSibling {
				t.Fatal("Undo touched unrelated row metadata")
			}
			// Scalar save and compensation omit rows, preserving every row/cell byte.
			keepTarget := storedKindRows(t, p, target)
			scalar := editorDecode[preferenceWriteResult](t, h.call(t, p, "PUT", base+q, editorJSON(map[string]any{"revision": rev, "residency": "eu", "residency_locked": locked, "prefs_locked": locked}), headers))
			before, after, _ = mutationProjection(t, p)
			if before.Residency != nil || after.Residency == nil || *after.Residency != "eu" || after.ResidencyLocked != locked || after.PrefsLocked != locked {
				t.Fatal("scalar audit wrong")
			}
			compensated := editorDecode[preferenceWriteResult](t, h.call(t, p, "PUT", base+q, editorJSON(map[string]any{"revision": scalar.Revision, "residency": nil, "residency_locked": false, "prefs_locked": false}), headers))
			if compensated.Level.Residency != nil || compensated.Level.ResidencyLocked || compensated.Level.PrefsLocked {
				t.Fatal("scalar Undo wrong")
			}
			if storedKindRows(t, p, archived.ID) != keepArchived || storedKindRows(t, p, sibling) != keepSibling || storedKindRows(t, p, target) != keepTarget {
				t.Fatal("scalar save/Undo rewrote rows or cells")
			}
			editorDecode[workKind](t, h.call(t, p, "POST", "/api/work-kinds/"+archived.ID+"/restore", "", nil))
			doc = h.prefs(t, p, project)
			if r := hasProjectedRow(*doc.Levels[level], archived.ID); r == nil || r.Normal != normal || r.Complex != complex || r.Locked != locked {
				t.Fatal("kind restore lost archived preferences")
			}
			if storedKindRows(t, p, archived.ID) != keepArchived {
				t.Fatal("kind restore altered row/cell metadata")
			}
		})
	}
}

func TestEditorRetiredKindUndoAndEventFailureRollback(t *testing.T) {
	p, h := editorFixture(t)
	created := h.call(t, p, "POST", "/api/work-kinds", `{"label":"Retiring"}`, nil)
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body.String())
	}
	var k workKind
	json.Unmarshal(created.Body.Bytes(), &k)
	path := "/api/model-preferences/levels/default/rows/" + k.ID
	headers := prefHeaders(p.ID)
	saved := editorDecode[preferenceWriteResult](t, h.call(t, p, "PUT", path, prefRowPayload(0, true, modelprefs.Cell{Mode: "auto"}, modelprefs.Cell{Mode: "auto"}), headers))
	reset := editorDecode[preferenceWriteResult](t, h.call(t, p, "DELETE", fmt.Sprintf("%s?revision=%d", path, saved.Revision), "", headers))
	editorDecode[workKind](t, h.call(t, p, "DELETE", "/api/work-kinds/"+k.ID, "", nil))
	before := preferenceStorage(t, p)
	for _, method := range []string{"PUT", "DELETE"} {
		uri, body := path, prefRowPayload(reset.Revision, true, modelprefs.Cell{Mode: "auto"}, modelprefs.Cell{Mode: "auto"})
		if method == "DELETE" {
			uri += fmt.Sprintf("?revision=%d", reset.Revision)
			body = ""
		}
		editorError(t, h.call(t, p, method, uri, body, headers), 422, "unknown_kind")
		if preferenceStorage(t, p) != before {
			t.Fatal("retired-kind refusal mutated rows/events")
		}
	}
	editorDecode[workKind](t, h.call(t, p, "POST", "/api/work-kinds/"+k.ID+"/restore", "", nil))
	// Inject at the actual SQL event insert, after resource writes.
	if _, err := adminPool.Exec(t.Context(), `CREATE SEQUENCE editor_event_probe; CREATE FUNCTION editor_event_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type IN ('model.preferences_changed','model.routes_replaced') THEN PERFORM nextval('editor_event_probe'); RAISE EXCEPTION 'editor injected event failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER editor_event_fail BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION editor_event_fail()`); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(t.Context(), `GRANT USAGE ON SEQUENCE editor_event_probe TO `+pgx.Identifier{testDB.Role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := adminPool.Exec(context.Background(), `DROP TRIGGER editor_event_fail ON events; DROP FUNCTION editor_event_fail(); DROP SEQUENCE editor_event_probe`)
		if err != nil {
			t.Error(err)
		}
	})
	before = preferenceStorage(t, p)
	for i, tc := range []struct{ path, body string }{{path, prefRowPayload(reset.Revision, true, modelprefs.Cell{Mode: "auto"}, modelprefs.Cell{Mode: "auto"})}, {"/api/model-preferences/levels/default", fmt.Sprintf(`{"revision":%d,"residency":"eu"}`, reset.Revision)}} {
		w := h.call(t, p, "PUT", tc.path, tc.body, headers)
		if w.Code != 500 {
			t.Fatal("event failure reported success", w.Code, w.Body.String())
		}
		if preferenceStorage(t, p) != before {
			t.Fatal("event failure left partial preference mutation")
		}
		var called bool
		var count int
		if err := adminPool.QueryRow(t.Context(), `SELECT is_called,last_value FROM editor_event_probe`).Scan(&called, &count); err != nil || !called || count != i+1 {
			t.Fatal("failure did not reach injected event insert", err)
		}
	}
	ladder := h.ladder(t, p, "build")
	routesBefore := catalogSetupState(t, p)
	w := h.call(t, p, "PUT", "/api/models/routes?role=build", "[]", map[string]string{"If-Match": *ladder.EditToken})
	if w.Code != 500 || catalogSetupState(t, p) != routesBefore {
		t.Fatal("event failure left partial ladder mutation", w.Code)
	}
}

// Wait for an observed database edge, rather than elapsed time, to prove overlap.
func waitEditorBlocked(t *testing.T, pid int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		var waiting bool
		if err := adminPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("no observed blocking edge")
		case <-poll.C:
		}
	}
}

func TestEditorMutationFinalPermissionRevocation(t *testing.T) {
	for _, ladder := range []bool{true, false} {
		t.Run(fmt.Sprint(ladder), func(t *testing.T) {
			p, h := editorFixture(t)
			path, body := "/api/model-preferences/levels/default", `{"revision":0,"residency":"eu"}`
			headers := prefHeaders(p.ID)
			if ladder {
				r := h.ladder(t, p, "build")
				path, body = "/api/models/routes?role=build", "[]"
				headers = map[string]string{"If-Match": *r.EditToken}
			}
			beforePref, beforeRoutes := preferenceStorage(t, p), catalogSetupState(t, p)
			barrier := &stalledEndpointBody{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(body)}
			r := httptest.NewRequest("PUT", path, barrier)
			r.AddCookie(h.cookie(t, p))
			for k, v := range headers {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { h.handler.ServeHTTP(w, r); close(done) }()
			<-barrier.entered
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				if err := db.LockTenant(t.Context(), tx, p.TenantID); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='member') WHERE principal_id=$1 AND scope_type='workspace'`, p.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			barrier.Close()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("revoked write hung")
			}
			if w.Code != 403 || strings.Contains(w.Body.String(), "stale_revision") {
				t.Fatal("wrong revocation refusal", w.Code, w.Body.String())
			}
			if preferenceStorage(t, p) != beforePref || catalogSetupState(t, p) != beforeRoutes {
				t.Fatal("revoked write changed state")
			}
		})
	}
}
