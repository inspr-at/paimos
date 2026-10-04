// SPDX-License-Identifier: AGPL-3.0-only
package themes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

type fixture struct {
	d                           *dbtest.DB
	s                           Store
	owner, member, other, agent tenant.Principal
}

func setup(t *testing.T) fixture {
	t.Helper()
	d := dbtest.Open(t)
	f := fixture{d: d, s: Store{d.App}}
	var tid string
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('themes','Themes') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	for i, target := range []*tenant.Principal{&f.owner, &f.member, &f.other, &f.agent} {
		*target = tenant.Principal{TenantID: tid, Kind: tenant.Person}
		if i == 3 {
			target.Kind = tenant.Agent
			target.Scopes = []string{"profile.read", "profile.write", "settings.manage"}
		}
		if err := d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,$3) RETURNING id::text`, tid, target.Kind, fmt.Sprintf("Theme actor %d", i)).Scan(&target.ID); err != nil {
			t.Fatal(err)
		}
		role := "member"
		if i == 0 {
			role = "owner"
		}
		dbtest.BindRole(t, d, tid, target.ID, role)
	}
	return f
}
func mustTheme(t *testing.T, s Store, p tenant.Principal, name, scope string) Theme {
	t.Helper()
	out, err := s.Create(t.Context(), p, CreateInput{Name: name, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func eventCount(t *testing.T, f fixture) int {
	t.Helper()
	var n int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type LIKE 'theme.%'`, f.owner.TenantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func lastEvent(t *testing.T, f fixture, typ string) events.Event {
	t.Helper()
	var e events.Event
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT id,actor_principal_id::text,type,before,after FROM events WHERE tenant_id=$1 AND type=$2 ORDER BY id DESC LIMIT 1`, f.owner.TenantID, typ).Scan(&e.ID, &e.ActorPrincipalID, &e.Type, &e.Before, &e.After); err != nil {
		t.Fatal(err)
	}
	return e
}
func call(t *testing.T, h http.Handler, p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if p.ID != "" {
		r = r.WithContext(tenant.WithPrincipal(t.Context(), p))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("HTTP %d want %d: %s", w.Code, status, w.Body.String())
	}
}
func undo(t *testing.T, f fixture, p tenant.Principal, e events.Event, status int) {
	t.Helper()
	mux := http.NewServeMux()
	events.New(f.d.App, events.WithUndoHandlers(UndoHandlers())).Mount(mux)
	expect(t, call(t, mux, p, "POST", fmt.Sprintf("/api/events/%d/undo", e.ID), ""), status)
}

func TestLifecycleCASAuditFallbackAndUndo(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	def, err := f.s.Active(ctx, f.member)
	if err != nil {
		t.Fatal(err)
	}
	if def.Theme.Name != "Porcelain" || def.Theme.Scope != "default" || def.Theme.OwnerPrincipalID != nil ||
		!same(def.Theme.Values, Porcelain()) || def.Revision != 0 || def.SelectedThemeID != nil {
		t.Fatalf("default: %+v", def)
	}
	if err := f.s.Delete(ctx, f.owner, def.Theme.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete default: %v", err)
	}
	name := "Workspace Porcelain"
	updated, err := f.s.Update(ctx, f.owner, def.Theme.ID, UpdateInput{Revision: 1, Name: &name})
	if err != nil || updated.Revision != 2 {
		t.Fatalf("default edit: %+v %v", updated, err)
	}
	undo(t, f, f.owner, lastEvent(t, f, "theme.updated"), 201)
	workspace := mustTheme(t, f.s, f.owner, "Workspace", "workspace")
	personal, err := f.s.Duplicate(ctx, f.member, workspace.ID, DuplicateInput{Name: "My Copper", Scope: "personal", Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if personal.OwnerPrincipalID == nil || *personal.OwnerPrincipalID != f.member.ID || !same(personal.Values, workspace.Values) {
		t.Fatalf("duplicate: %+v", personal)
	}
	if _, err := f.s.Duplicate(ctx, f.member, workspace.ID, DuplicateInput{Name: "Stale", Scope: "personal", Revision: 99}); !errors.Is(err, ErrConflict) {
		t.Fatalf("copy CAS: %v", err)
	}
	beforeEvents := eventCount(t, f)
	v := personal.Values
	v.Primary.Light, v.Primary.Dark = "#ffffff", nil // poor contrast is accepted; derived dark is retained.
	v.Secondary.Dark = nil
	custom := "#bf3d6d"
	v.RecurringMarker = Marker{Source: "custom", Custom: &custom}
	size, ring := 30, "off"
	v.Agents = Agents{Avatar: "quill", Ring: &ring, Size: &size, Hover: true, Palette: "deutan"}
	personal, err = f.s.Update(ctx, f.member, personal.ID, UpdateInput{Revision: 1, Values: &v})
	if err != nil || personal.Revision != 2 || !same(personal.Values, v) {
		t.Fatalf("values: %+v %v", personal, err)
	}
	e := lastEvent(t, f, "theme.updated")
	var b, a Theme
	if json.Unmarshal(e.Before, &b) != nil || json.Unmarshal(e.After, &a) != nil || b.Revision != 1 || a.Revision != 2 || e.ActorPrincipalID != f.member.ID || !same(a, personal) {
		t.Fatalf("audit: %+v", e)
	}
	if _, err := f.s.Update(ctx, f.member, personal.ID, UpdateInput{Revision: 2, Values: &v}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Update(ctx, f.member, personal.ID, UpdateInput{Revision: 1, Values: &v}); !errors.Is(err, ErrConflict) {
		t.Fatalf("update CAS: %v", err)
	}
	if err := f.s.Delete(ctx, f.member, personal.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete CAS: %v", err)
	}
	if n := eventCount(t, f); n != beforeEvents+1 {
		t.Fatalf("failed/no-op writes appended events: %d -> %d", beforeEvents, n)
	}
	undo(t, f, f.member, e, 201)
	personal, err = f.s.Get(ctx, f.member, personal.ID)
	if err != nil || personal.Revision != 3 || !same(personal.Values, workspace.Values) {
		t.Fatalf("undo values: %+v %v", personal, err)
	}
	undo(t, f, f.member, e, 409)
	upperID := strings.ToUpper(personal.ID)
	chosen, err := f.s.Select(ctx, f.member, SelectionInput{ThemeID: &upperID})
	if err != nil || chosen.Theme.ID != personal.ID || chosen.Revision != 1 {
		t.Fatalf("select: %+v %v", chosen, err)
	}
	var selected Selection
	if err := json.Unmarshal(lastEvent(t, f, "theme.selected").After, &selected); err != nil || selected.ThemeID == nil || *selected.ThemeID != personal.ID {
		t.Fatalf("non-canonical selection audit: %+v %v", selected, err)
	}
	selectedEvents := eventCount(t, f)
	if chosen, err := f.s.Select(ctx, f.member, SelectionInput{ThemeID: &personal.ID, Revision: 1}); err != nil || chosen.Revision != 1 || eventCount(t, f) != selectedEvents {
		t.Fatalf("UUID spelling changed selection: %+v %v", chosen, err)
	}
	if _, err := f.s.Select(ctx, f.member, SelectionInput{ThemeID: &workspace.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("selection CAS: %v", err)
	}
	if err := f.s.Delete(ctx, f.member, personal.ID, personal.Revision); err != nil {
		t.Fatal(err)
	}
	deleted := lastEvent(t, f, "theme.deleted")
	chosen, err = f.s.Active(ctx, f.member)
	if err != nil || chosen.Theme.ID != def.Theme.ID || chosen.FallbackNotice == nil || chosen.FallbackNotice.DeletedThemeID != personal.ID || chosen.Revision != 1 {
		t.Fatalf("fallback: %+v %v", chosen, err)
	}
	if _, err := f.s.Get(ctx, f.member, personal.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted visible: %v", err)
	}
	if _, err := f.s.Select(ctx, f.member, SelectionInput{ThemeID: &personal.ID, Revision: 1}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("selected tombstone: %v", err)
	}
	// Choosing a new theme while the old one is deleted survives deletion undo.
	chosen, err = f.s.Select(ctx, f.member, SelectionInput{ThemeID: &workspace.ID, Revision: 1})
	if err != nil || chosen.FallbackNotice != nil {
		t.Fatalf("replace fallback: %+v %v", chosen, err)
	}
	newChoiceGeneration := chosen.Revision
	assertActive(t, f, f.member, workspace.ID, 2)
	undo(t, f, f.member, deleted, 201)
	chosen, err = f.s.Active(ctx, f.member)
	if err != nil || chosen.Theme.ID != workspace.ID || chosen.Revision != newChoiceGeneration {
		t.Fatalf("undo overwrote choice: %+v %v", chosen, err)
	}
	undo(t, f, f.member, lastEvent(t, f, "theme.selected"), 201)
	chosen, err = f.s.Active(ctx, f.member)
	if err != nil || chosen.Theme.ID != personal.ID || chosen.Revision == newChoiceGeneration {
		t.Fatalf("selection undo: %+v %v", chosen, err)
	}
	assertActive(t, f, f.member, personal.ID, 3)
	created := mustTheme(t, f.s, f.member, "Undo creation", "personal")
	undo(t, f, f.member, lastEvent(t, f, "theme.created"), 201)
	if _, err := f.s.Get(ctx, f.member, created.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("undo create: %v", err)
	}
}

func TestPermissionsPrivacyAndTenantIsolation(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	private := mustTheme(t, f.s, f.member, "Private Copper", "personal")
	workspace := mustTheme(t, f.s, f.owner, "Everyone", "workspace")
	chosen, err := f.s.Select(ctx, f.member, SelectionInput{ThemeID: &private.ID})
	if err != nil || chosen.Theme.ID != private.ID {
		t.Fatal(err)
	}
	for _, p := range []tenant.Principal{f.other, f.owner, f.agent} {
		if _, err := f.s.Get(ctx, p, private.ID); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("private theme exposed to %s: %v", p.ID, err)
		}
		if _, err := f.s.Update(ctx, p, private.ID, UpdateInput{Revision: 1, Name: &private.Name}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("private write: %v", err)
		}
		page, err := f.s.List(ctx, p, "", 100)
		if err != nil || len(page.Items) != 2 {
			t.Fatalf("visible page: %+v %v", page, err)
		}
		if err := db.InTenant(tenant.WithPrincipal(ctx, p), f.d.App, p.TenantID, func(tx pgx.Tx) error {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type LIKE 'theme.%' AND metadata->>'audience_principal_id'=$1`, f.member.ID).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Fatalf("private audit exposed: %d", n)
			}
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM theme_selections WHERE principal_id=$1`, f.member.ID).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Fatal("another person's choice exposed")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		events.New(f.d.App).Mount(mux)
		w := call(t, mux, p, "GET", "/api/events", "")
		expect(t, w, 200)
		if strings.Contains(w.Body.String(), "Private Copper") || strings.Contains(w.Body.String(), private.ID) {
			t.Fatal("history leaked private theme")
		}
	}
	name := "Denied"
	for _, p := range []tenant.Principal{f.member, f.other} {
		if _, err := f.s.Create(ctx, p, CreateInput{Name: name, Scope: "workspace"}); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("workspace create: %v", err)
		}
		if _, err := f.s.Update(ctx, p, workspace.ID, UpdateInput{Revision: 1, Name: &name}); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("workspace edit: %v", err)
		}
		if err := f.s.Delete(ctx, p, workspace.ID, 1); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("workspace delete: %v", err)
		}
	}
	if _, err := f.s.Select(ctx, f.other, SelectionInput{ThemeID: &private.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("choose other's theme: %v", err)
	}
	active, err := f.s.Active(ctx, f.agent)
	if err != nil || active.Theme.Scope != "default" {
		t.Fatalf("agent active: %+v %v", active, err)
	}
	for _, scope := range []string{"personal", "workspace"} {
		if _, err := f.s.Create(ctx, f.agent, CreateInput{Name: "Agent write", Scope: scope}); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("agent write %s: %v", scope, err)
		}
	}
	if _, err := f.s.Select(ctx, f.agent, SelectionInput{ThemeID: &workspace.ID}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("agent choice: %v", err)
	}
	limited := f.agent
	limited.Scopes = nil
	if _, err := f.s.Active(ctx, limited); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("agent without scope: %v", err)
	}
	// A second tenant has its own seeded default and cannot name the first's themes.
	foreign := tenant.Principal{Kind: tenant.Person}
	if err := f.d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('foreign','Foreign') RETURNING id::text`).Scan(&foreign.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := f.d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Foreign') RETURNING id::text`, foreign.TenantID).Scan(&foreign.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, foreign.TenantID, foreign.ID, "owner")
	if _, err := f.s.Get(ctx, foreign, workspace.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross tenant read: %v", err)
	}
	if _, err := f.s.Select(ctx, foreign, SelectionInput{ThemeID: &workspace.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross tenant select: %v", err)
	}
	if err := db.InTenant(tenant.WithPrincipal(ctx, foreign), f.d.App, foreign.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO themes(tenant_id,name,scope,config) VALUES($1,'Cross','workspace',$2)`, f.owner.TenantID, mustJSON(t, Porcelain()))
		return err
	}); err == nil {
		t.Fatal("cross tenant insert bypassed RLS")
	} else {
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "42501" {
			t.Fatalf("wrong isolation failure: %v", err)
		}
	}
	if _, err := f.d.Admin.Exec(ctx, `UPDATE themes SET scope='workspace' WHERE tenant_id=$1 AND scope='default'`, f.owner.TenantID); err == nil {
		t.Fatal("default converted")
	}
	if _, err := f.d.Admin.Exec(ctx, `DELETE FROM themes WHERE tenant_id=$1 AND scope='default'`, f.owner.TenantID); err == nil {
		t.Fatal("default physically deleted")
	}
}
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestKeysetPaginationAndValidationHTTP(t *testing.T) {
	f := setup(t)
	for i := 0; i < 4; i++ {
		mustTheme(t, f.s, f.member, fmt.Sprintf("Mine %d", i), "personal")
	}
	seen := map[string]bool{}
	after := ""
	for {
		page, err := f.s.List(t.Context(), f.member, after, 2)
		if err != nil || len(page.Items) > 2 {
			t.Fatalf("page: %+v %v", page, err)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatal("duplicate page record")
			}
			seen[item.ID] = true
		}
		if page.NextCursor == nil {
			break
		}
		after = *page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("missing themes: %d", len(seen))
	}
	mux := http.NewServeMux()
	New(f.d.App).Mount(mux)
	expect(t, call(t, mux, tenant.Principal{}, "GET", "/api/themes", ""), 401)
	for _, q := range []string{"?limit=0", "?limit=101", "?limit=x", "?after=bad"} {
		expect(t, call(t, mux, f.member, "GET", "/api/themes"+q, ""), 400)
	}
	for _, body := range []string{
		`null`, `{}`, `{"name":"Bad","scope":"default"}`, `{"name":" Bad ","scope":"personal"}`,
		`{"name":"Bad","scope":"personal","owner_principal_id":"other"}`, `{"name":"Bad","scope":"personal","values":null}`,
		`{"name":"Bad","scope":"personal","values":{}}`, `{"name":"Bad","scope":"personal"} {}`,
	} {
		expect(t, call(t, mux, f.member, "POST", "/api/themes", body), 400)
	}
	expect(t, call(t, mux, f.member, "POST", "/api/themes", strings.Repeat(" ", 8193)), 413)
	expect(t, call(t, mux, f.member, "PUT", "/api/me/theme", `{"revision":0}`), 400)
	expect(t, call(t, mux, f.member, "PUT", "/api/me/theme", `{"theme_id":null,"revision":0}`), 200)
	expect(t, call(t, mux, f.member, "GET", "/api/themes/not-an-id", ""), 400)
	expect(t, call(t, mux, f.member, "DELETE", "/api/themes/not-an-id?revision=1", ""), 400)
	base := Porcelain()
	for _, mutate := range []func(*Values){
		func(v *Values) { v.Primary.Light = "red" }, func(v *Values) { v.RecurringMarker.Source = "custom" },
		func(v *Values) { v.Agents.Avatar = "unknown" }, func(v *Values) { v.Agents.Palette = "unknown" },
		func(v *Values) { size := 101; v.Agents.Size = &size }, func(v *Values) { ring := "spin"; v.Agents.Ring = &ring },
	} {
		v := base
		mutate(&v)
		if _, err := f.s.Create(t.Context(), f.member, CreateInput{Name: "Invalid", Scope: "personal", Values: &v}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid values: %v", err)
		}
	}
	for _, style := range []string{"pulse", "robot-1", "robot-2", "robot-3", "robot-4", "robot-5", "orbit", "quill", "sprite"} {
		v := base
		v.Agents.Avatar = style
		if err := v.validate(); err != nil {
			t.Fatalf("existing avatar %s: %v", style, err)
		}
	}
}

func TestConcurrentCASAndRevocationFence(t *testing.T) {
	t.Run("CAS", func(t *testing.T) {
		f := setup(t)
		theme := mustTheme(t, f.s, f.member, "Initial", "personal")
		pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(sql string) bool { return strings.HasPrefix(sql, "UPDATE themes SET") })
		s := Store{pool}
		first := make(chan error, 1)
		second := make(chan error, 1)
		done := make(chan struct{})
		name1, name2 := "First", "Second"
		go func() {
			_, err := s.Update(ctx, f.member, theme.ID, UpdateInput{Revision: 1, Name: &name1})
			first <- err
		}()
		pid := barrier.Wait(t, ctx)
		go func() {
			_, err := s.Update(ctx, f.member, theme.ID, UpdateInput{Revision: 1, Name: &name2})
			second <- err
			close(done)
		}()
		if dbtest.BlockedOrDone(t, ctx, f.d.Admin, pid, done) == "" {
			t.Fatal("writers did not overlap")
		}
		barrier.Release()
		if err := dbtest.Await(t, ctx, first); err != nil {
			t.Fatal(err)
		}
		if err := dbtest.Await(t, ctx, second); !errors.Is(err, ErrConflict) {
			t.Fatalf("loser CAS: %v", err)
		}
		current, err := f.s.Get(ctx, f.member, theme.ID)
		if err != nil || current.Name != name1 || current.Revision != 2 {
			t.Fatalf("lost update: %+v %v", current, err)
		}
	})
	t.Run("revocation", func(t *testing.T) {
		f := setup(t)
		dbtest.BindRole(t, f.d, f.other.TenantID, f.other.ID, "owner")
		theme := mustTheme(t, f.s, f.owner, "Workspace", "workspace")
		// Hold a real access-change lock before the writer can acquire the tree.
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		pool := f.d.App
		tx, err := f.d.Admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR UPDATE`, f.owner.TenantID); err != nil {
			t.Fatal(err)
		}
		name := "Must fail"
		result := make(chan error, 1)
		done := make(chan struct{})
		before := eventCount(t, f)
		go func() {
			_, err := (Store{pool}).Update(ctx, f.owner, theme.ID, UpdateInput{Revision: 1, Name: &name})
			result <- err
			close(done)
		}()
		if dbtest.BlockedOrDone(t, ctx, f.d.Admin, tx.Conn().PgConn().PID(), done) == "" {
			t.Fatal("writer did not wait for authority lock")
		}
		if _, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='member') WHERE tenant_id=$1 AND principal_id=$2 AND scope_type='workspace'`, f.owner.TenantID, f.owner.ID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := dbtest.Await(t, ctx, result); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("revoked write: %v", err)
		}
		current, err := f.s.Get(ctx, f.owner, theme.ID)
		if err != nil || current.Name != "Workspace" || current.Revision != 1 || eventCount(t, f) != before {
			t.Fatalf("revoked mutation committed: %+v %v", current, err)
		}
	})
}
