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

func TestEditorConcurrentConditionalLegacyAndRevocationFence(t *testing.T) {
	for _, mode := range []string{"conditional", "legacy", "revoke"} {
		t.Run(mode, func(t *testing.T) {
			p, _ := editorFixture(t)
			held, release := make(chan int, 1), make(chan struct{})
			defer close(release)
			calls := 0
			m := &Module{pool: appPool, validationClock: func(ctx context.Context, tx pgx.Tx) (time.Time, error) {
				calls++
				if calls == 1 {
					var pid int
					if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
						return time.Time{}, err
					}
					held <- pid
					select {
					case <-release:
					case <-ctx.Done():
						return time.Time{}, ctx.Err()
					}
				}
				return validationNow(ctx, tx)
			}}
			h := newEditorHTTP(t, m)
			initial := h.ladder(t, p, "build")
			wanted := append([]Route{}, initial.Routes...)
			wanted[0].Priority = 500
			first := startEditorRequest(t, h, p, "PUT", "/api/models/routes?role=build", editorJSON(wanted), map[string]string{"If-Match": *initial.EditToken})
			var pid int
			select {
			case pid = <-held:
			case <-time.After(10 * time.Second):
				t.Fatal("writer did not reach post-fence clock")
			}
			var second <-chan *httptest.ResponseRecorder
			revoked := make(chan error, 1)
			if mode == "revoke" {
				go func() {
					revoked <- db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
						if err := db.LockTenant(t.Context(), tx, p.TenantID); err != nil {
							return err
						}
						_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='member') WHERE principal_id=$1 AND scope_type='workspace'`, p.ID)
						return err
					})
				}()
			} else {
				path := "/api/models/routes?role=build"
				headers := map[string]string{"If-Match": *initial.EditToken}
				if mode == "legacy" {
					path = "/api/models/routes"
					headers = nil
				}
				second = startEditorRequest(t, h, p, "PUT", path, "[]", headers)
			}
			waitEditorBlocked(t, pid)
			// Exactly one release without risking a cleanup double-close.
			release <- struct{}{}
			editorDecode[[]Route](t, finishEditorRequest(t, first))
			if mode == "revoke" {
				select {
				case err := <-revoked:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("revoker remained blocked")
				}
				if len(h.ladder(t, p, "build").Routes) != len(wanted) {
					t.Fatal("authorized fenced mutation did not commit")
				}
				return
			}
			w := finishEditorRequest(t, second)
			if mode == "conditional" {
				editorError(t, w, 409, "stale_revision")
				if *h.ladder(t, p, "build").EditToken != routeEditToken("build", wanted) {
					t.Fatal("stale competing writer overwrote first")
				}
			} else {
				if len(editorDecode[[]Route](t, w)) != 0 || len(h.ladder(t, p, "build").Routes) != 0 {
					t.Fatal("legacy write did not retain whole-tenant semantics")
				}
			}
		})
	}
}

func TestEditorPostFenceExpiryClearAfterWaiting(t *testing.T) {
	p, h := editorFixture(t)
	initial := h.ladder(t, p, "build")
	expiry := time.Date(2030, 1, 1, 0, 0, 0, 654321000, time.UTC)
	desired := append([]Route{}, initial.Routes...)
	desired[0].State, desired[0].Reason, desired[0].ValidUntil = "unavailable", "previous hold", &expiry
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
			var pid int
			blocker.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid)
			clockCalls := make(chan struct{}, 1)
			m := &Module{pool: appPool, validationClock: func(ctx context.Context, tx pgx.Tx) (time.Time, error) { clockCalls <- struct{}{}; return expiry, nil }}
			h = newEditorHTTP(t, m)
			path := "/api/models/routes?role=build"
			if clear {
				path += "&expiry_policy=clear"
			}
			done := startEditorRequest(t, h, p, "PUT", path, editorJSON(desired), map[string]string{"If-Match": *initial.EditToken})
			waitEditorBlocked(t, pid)
			select {
			case <-clockCalls:
				t.Fatal("clock sampled before tenant fence acquired")
			default:
			}
			if err := blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			w := finishEditorRequest(t, done)
			select {
			case <-clockCalls:
			default:
				t.Fatal("post-fence clock not sampled")
			}
			if clear {
				actual := editorDecode[[]Route](t, w)
				if actual[0].State != "available" || actual[0].ValidUntil != nil || actual[0].Reason != "" {
					t.Fatal("equal expiry compensation revived hold")
				}
			} else {
				editorError(t, w, 422, "suppression_expired")
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

func TestEditorConditionalWriteWaitsForAdditivePreparation(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "editor-upgrade", "person", "Owner", []string{"admin"})
	incompleteCatalogFixture(t, p)
	h := newEditorHTTP(t, &Module{pool: appPool})
	old := h.ladder(t, p, "build")
	held, release := make(chan int, 1), make(chan struct{})
	defer close(release)
	calls := 0
	r := httptest.NewRequest("GET", "/api/models", nil)
	done := make(chan error, 1)
	go func() {
		done <- PrepareCatalog(t.Context(), appPool, p, CatalogPreparation{Operation: CatalogRead, Request: r, Clock: func(ctx context.Context, tx pgx.Tx) (time.Time, error) {
			calls++
			if calls == 1 {
				var pid int
				if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
					return time.Time{}, err
				}
				held <- pid
				select {
				case <-release:
				case <-ctx.Done():
					return time.Time{}, ctx.Err()
				}
			}
			return validationNow(ctx, tx)
		}})
	}()
	var pid int
	select {
	case pid = <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("preparation did not hold fence")
	}
	write := startEditorRequest(t, h, p, "PUT", "/api/models/routes?role=build", "[]", map[string]string{"If-Match": *old.EditToken})
	waitEditorBlocked(t, pid)
	release <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("preparation hung")
	}
	editorError(t, finishEditorRequest(t, write), 409, "stale_revision")
	current := h.ladder(t, p, "build")
	if *current.EditToken == *old.EditToken || len(current.Routes) <= len(old.Routes) || current.Routes[0].ProfileID != old.Routes[0].ProfileID || current.Routes[0].Priority != 17 {
		t.Fatal("upgrade overwrote saved priority or snapshot missed additions")
	}
}
