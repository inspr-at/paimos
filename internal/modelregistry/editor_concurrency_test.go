// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func startEditorRequest(t *testing.T, h *editorHTTP, p tenant.Principal, method, path, body string, headers map[string]string) <-chan *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(h.cookie(t, p))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	t.Cleanup(cancel)
	r = r.WithContext(ctx)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); h.handler.ServeHTTP(w, r); done <- w }()
	return done
}
func finishEditorRequest(t *testing.T, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case w := <-done:
		return w
	case <-time.After(15 * time.Second):
		t.Fatal("HTTP mutation hung")
		return nil
	}
}

// Risk: the retired ladder writer still decodes, fences, waits or writes. With
// an edit token, a whole-tenant legacy body or a revocation holding the tenant
// fence it used to take, PUT answers 410 without waiting, never reaches the
// post-fence validation clock, and leaves storage unchanged (approved Models
// v2 design: PUT /models/routes is 410 Gone after the migration).
func TestEditorConcurrentConditionalLegacyAndRevocationFence(t *testing.T) {
	for _, mode := range []string{"conditional", "legacy", "revoke"} {
		t.Run(mode, func(t *testing.T) {
			p, _ := editorFixture(t)
			var clocked atomic.Int32
			m := &Module{pool: appPool, validationClock: func(ctx context.Context, tx pgx.Tx) (time.Time, error) {
				clocked.Add(1)
				return validationNow(ctx, tx)
			}}
			h := newEditorHTTP(t, m)
			initial := h.ladder(t, p, "build")
			if initial.CanEdit || initial.EditToken != nil {
				t.Fatalf("derived ladder offered an edit token: %+v", initial)
			}
			before := catalogSetupState(t, p)
			held, release, fenced := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				fenced <- db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
					if err := db.LockTenant(t.Context(), tx, p.TenantID); err != nil {
						return err
					}
					if mode == "revoke" {
						if _, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='member') WHERE principal_id=$1 AND scope_type='workspace'`, p.ID); err != nil {
							return err
						}
					}
					close(held)
					<-release
					return nil
				})
			}()
			select {
			case <-held:
			case err := <-fenced:
				t.Fatal("fence holder failed", err)
			case <-time.After(10 * time.Second):
				t.Fatal("fence holder did not take the tenant fence")
			}
			path, body := "/api/models/routes?role=build", editorJSON(initial.Routes)
			headers := map[string]string{"If-Match": routeEditToken("build", initial.Routes)}
			if mode == "legacy" {
				path, body, headers = "/api/models/routes", "[]", nil
			}
			w := finishEditorRequest(t, startEditorRequest(t, h, p, "PUT", path, body, headers))
			close(release)
			if err := <-fenced; err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusGone || w.Header().Get("Deprecation") != "true" || !strings.Contains(w.Body.String(), "Settings > Models") {
				t.Fatalf("retired writer answered %d: %s", w.Code, w.Body.String())
			}
			if clocked.Load() != 0 {
				t.Fatal("retired writer reached the post-fence validation clock")
			}
			if catalogSetupState(t, p) != before {
				t.Fatal("retired writer changed storage")
			}
		})
	}
}

// Risk: the retired writer still waits on the tenant fence or samples its
// post-fence clock for a hold or an expiry compensation (holds are model pauses).
func TestEditorPostFenceExpiryClearAfterWaiting(t *testing.T) {
	p, _ := editorFixture(t)
	expiry := time.Date(2030, 1, 1, 0, 0, 0, 654321000, time.UTC)
	for _, clear := range []bool{false, true} {
		t.Run(fmt.Sprint(clear), func(t *testing.T) {
			blocker, err := appPool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if err := db.LockTenant(t.Context(), blocker, p.TenantID); err != nil {
				t.Fatal(err)
			}
			var clocked atomic.Int32
			h := newEditorHTTP(t, &Module{pool: appPool, validationClock: func(context.Context, pgx.Tx) (time.Time, error) { clocked.Add(1); return expiry, nil }})
			initial := h.ladder(t, p, "build")
			desired := append([]Route{}, initial.Routes...)
			if len(desired) == 0 {
				t.Fatal("fixture has no derived build route")
			}
			desired[0].State, desired[0].Reason, desired[0].ValidUntil = "unavailable", "previous hold", &expiry
			path := "/api/models/routes?role=build"
			if clear {
				path += "&expiry_policy=clear"
			}
			// Answered while the tenant fence is still held: nothing waits.
			w := finishEditorRequest(t, startEditorRequest(t, h, p, "PUT", path, editorJSON(desired), map[string]string{"If-Match": routeEditToken("build", initial.Routes)}))
			if w.Code != http.StatusGone || clocked.Load() != 0 {
				t.Fatalf("retired writer waited or sampled its clock: %d %s", w.Code, w.Body.String())
			}
			if err := blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEditorPreferenceGETCoherentSnapshotAcrossLink(t *testing.T) {
	p, h := editorFixture(t)
	session := addPrincipal(t, p.TenantID, "person", "Snapshot session", []string{"member"})
	canonical := addPrincipal(t, p.TenantID, "person", "Canonical", []string{"member"})
	editorDecode[preferenceWriteResult](t, h.call(t, session, "PUT", "/api/model-preferences/levels/person", `{"revision":0,"residency":"eu"}`, prefHeaders(session.ID)))
	editorDecode[preferenceWriteResult](t, h.call(t, canonical, "PUT", "/api/model-preferences/levels/person", `{"revision":0,"residency":"local"}`, prefHeaders(canonical.ID)))
	blocker, err := adminPool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(t.Context(), `LOCK TABLE model_pref_scopes IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	var pid int
	blocker.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid)
	done := startEditorRequest(t, h, session, "GET", "/api/model-preferences", "", nil)
	waitEditorBlocked(t, pid)
	linkEditorPerson(t, p, session.ID, canonical.ID)
	if err := blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	doc := editorDecode[preferenceDocument](t, finishEditorRequest(t, done))
	if doc.PersonID == nil || *doc.PersonID != session.ID || doc.Levels["person"].Residency == nil || *doc.Levels["person"].Residency != "eu" || doc.Views["person"].Residency.Value != "eu" || doc.Levels["person"].Revision != 1 || !doc.Can["edit_person"] {
		t.Fatal("GET mixed identity, levels, views or permissions across snapshots")
	}
	fresh := h.prefs(t, session, "")
	if fresh.PersonID == nil || *fresh.PersonID != canonical.ID || *fresh.Levels["person"].Residency != "local" {
		t.Fatal("fresh linked read not canonical")
	}
}

func TestEditorArchiveWaitsBehindFencedRowUndo(t *testing.T) {
	p, h := editorFixture(t)
	w := h.call(t, p, "POST", "/api/work-kinds", `{"label":"Archive after Undo"}`, nil)
	if w.Code != 201 {
		t.Fatal(w.Code)
	}
	var k workKind
	if err := json.Unmarshal(w.Body.Bytes(), &k); err != nil {
		t.Fatal(err)
	}
	path := "/api/model-preferences/levels/default/rows/" + k.ID
	headers := prefHeaders(p.ID)
	body := prefRowPayload(0, true, modelprefs.Cell{Mode: "auto"}, modelprefs.Cell{Mode: "auto"})
	saved := editorDecode[preferenceWriteResult](t, h.call(t, p, "PUT", path, body, headers))
	reset := editorDecode[preferenceWriteResult](t, h.call(t, p, "DELETE", fmt.Sprintf("%s?revision=%d", path, saved.Revision), "", headers))
	blocker, err := adminPool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(t.Context(), `LOCK TABLE model_pref_rows IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	var pid int
	blocker.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid)
	undo := startEditorRequest(t, h, p, "PUT", path, prefRowPayload(reset.Revision, true, modelprefs.Cell{Mode: "auto"}, modelprefs.Cell{Mode: "auto"}), headers)
	waitEditorBlocked(t, pid)
	// The row query is after Undo's tenant fence; observe archive waiting on it.
	var writerPID int
	if err := adminPool.QueryRow(t.Context(), `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) LIMIT 1`, pid).Scan(&writerPID); err != nil {
		t.Fatal(err)
	}
	archive := startEditorRequest(t, h, p, "DELETE", "/api/work-kinds/"+k.ID, "", nil)
	waitEditorBlocked(t, writerPID)
	if err := blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	result := editorDecode[preferenceWriteResult](t, finishEditorRequest(t, undo))
	if hasProjectedRow(result.Level, k.ID) == nil {
		t.Fatal("Undo not restored while active")
	}
	editorDecode[workKind](t, finishEditorRequest(t, archive))
	keep := storedKindRows(t, p, k.ID)
	editorDecode[workKind](t, h.call(t, p, "POST", "/api/work-kinds/"+k.ID+"/restore", "", nil))
	doc := h.prefs(t, p, "")
	if hasProjectedRow(*doc.Levels["default"], k.ID) == nil || storedKindRows(t, p, k.ID) != keep {
		t.Fatal("archive after Undo deleted restored values")
	}
}

// Risk: the retired writer waits on additive catalog preparation, or the
// preparation rewrites a saved Models choice. PUT is 410 while preparation
// holds the fence; preparation then adds profiles and leaves the order alone.
func TestEditorConditionalWriteWaitsForAdditivePreparation(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "editor-upgrade", "person", "Owner", []string{"admin"})
	incompleteCatalogFixture(t, p)
	h := newEditorHTTP(t, &Module{pool: appPool})
	old := h.ladder(t, p, "build")
	orders := func() string {
		t.Helper()
		var state string
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT jsonb_build_object('orders',(SELECT jsonb_agg(to_jsonb(o) ORDER BY profile_id,column_key,situation) FROM (`+modelprefs.OrdersSQL+`) o),
 'profiles',(SELECT count(*) FROM model_profiles))::text`).Scan(&state)
		}); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := orders()
	held, release := make(chan struct{}), make(chan struct{})
	calls := 0
	r := httptest.NewRequest("GET", "/api/models", nil)
	done := make(chan error, 1)
	go func() {
		done <- PrepareCatalog(t.Context(), appPool, p, CatalogPreparation{Operation: CatalogRead, Request: r, Clock: func(ctx context.Context, tx pgx.Tx) (time.Time, error) {
			calls++
			if calls == 1 {
				close(held)
				select {
				case <-release:
				case <-ctx.Done():
					return time.Time{}, ctx.Err()
				}
			}
			return validationNow(ctx, tx)
		}})
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("preparation did not hold fence")
	}
	w := finishEditorRequest(t, startEditorRequest(t, h, p, "PUT", "/api/models/routes?role=build", "[]", map[string]string{"If-Match": routeEditToken("build", old.Routes)}))
	close(release)
	if w.Code != http.StatusGone {
		t.Fatalf("retired writer answered %d: %s", w.Code, w.Body.String())
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("preparation hung")
	}
	after := orders()
	if after == before || !strings.HasPrefix(after, before[:strings.Index(before, `"profiles"`)]) {
		t.Fatalf("preparation rewrote the saved Default or added no profiles: %s → %s", before, after)
	}
	current := h.ladder(t, p, "build")
	if len(current.Routes) == 0 || len(old.Routes) == 0 || current.Routes[0].ProfileID != old.Routes[0].ProfileID {
		t.Fatal("saved Default no longer leads the derived build read")
	}
}
