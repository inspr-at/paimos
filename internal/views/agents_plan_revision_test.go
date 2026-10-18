// SPDX-License-Identifier: AGPL-3.0-only

package views

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestAgentsPlanRevisionCheckedSaves(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var tid string
	must(d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('plan-revisions','Revisions') RETURNING id::text`).Scan(&tid))
	person := func(name string, linked *string) tenant.Principal {
		t.Helper()
		p := tenant.Principal{TenantID: tid, Kind: tenant.Person}
		must(d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,linked_to) VALUES($1,'person',$2,$3) RETURNING id::text`, tid, name, linked).Scan(&p.ID))
		dbtest.BindRole(t, d, tid, p.ID, "member")
		return p
	}
	owner := person("Canonical", nil)
	alias := person("Alias", &owner.ID)
	other := person("Unrelated person", nil)
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	request := func(p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(tenant.WithPrincipal(ctx, p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	read := func(p tenant.Principal) preference {
		t.Helper()
		w := request(p, "GET", "/api/preferences/agents.working", "")
		if w.Code != 200 {
			t.Fatalf("read status=%d body=%s", w.Code, w.Body.String())
		}
		var out preference
		must(json.Unmarshal(w.Body.Bytes(), &out))
		return out
	}
	saveBody := func(total int, revision preference) string {
		t.Helper()
		body, err := json.Marshal(map[string]any{"value": map[string]any{"total": total, "limits": map[string]any{"claude": "off"}}, "expected_updated_at": revision.UpdatedAt})
		must(err)
		return string(body)
	}
	assertStatus := func(p tenant.Principal, body string, status int) {
		t.Helper()
		w := request(p, "PUT", "/api/preferences/agents.working", body)
		if w.Code != status {
			t.Fatalf("save status=%d want=%d body=%s", w.Code, status, w.Body.String())
		}
	}
	unset := read(owner)
	if unset.UpdatedAt != nil {
		t.Fatal("new family has a saved revision")
	}
	assertStatus(alias, saveBody(5, unset), 200)
	first := read(owner)
	if first.UpdatedAt == nil {
		t.Fatal("save did not return a revision")
	}
	assertStatus(owner, saveBody(6, unset), 409)
	// An unconditional old CLI remains supported, and invalidates the UI's
	// previous revision. Alias copies are reconciled in the same transaction.
	_, err := d.Admin.Exec(ctx, `INSERT INTO user_preferences(tenant_id,principal_id,key,value,updated_at) VALUES($1,$2,'agents.working',$3::jsonb,$4)`, tid, alias.ID, string(first.Value), first.UpdatedAt)
	must(err)
	assertStatus(alias, `{"value":{"total":7,"limits":{"claude":"off"}}}`, 200)
	second := read(owner)
	if !second.UpdatedAt.After(*first.UpdatedAt) {
		t.Fatal("revision did not advance")
	}
	assertStatus(owner, saveBody(4, first), 409)
	if current := read(owner); string(current.Value) != string(second.Value) || !current.UpdatedAt.Equal(*second.UpdatedAt) {
		t.Fatal("stale save changed the plan")
	}
	if current := read(alias); string(current.Value) != string(second.Value) || !current.UpdatedAt.Equal(*second.UpdatedAt) {
		t.Fatal("alias plan diverged")
	}
	// Revision checks are scoped to the caller's canonical family.
	assertStatus(other, saveBody(3, unset), 200)
	assertStatus(owner, saveBody(8, second), 200)
	assertStatus(owner, `{"value":{"total":8},"expected_updated_at":"invalid"}`, 400)
	assertStatus(owner, `{"value":{"total":8},"expected_updated_at":42}`, 400)
	if w := request(owner, "PUT", "/api/preferences/agents.working.display", `{"value":{"folded":true},"expected_updated_at":null}`); w.Code != 400 {
		t.Fatalf("plan precondition accepted on another preference: %d", w.Code)
	}

	t.Run("simultaneous canonical and alias saves have one winner", func(t *testing.T) {
		before := read(owner)
		bodies := []string{saveBody(9, before), saveBody(10, before)}
		// Pause after the first writer has consumed the old revision, before
		// its upsert acquires a preference-row lock. Only the tenant fence can
		// prevent the alias from accepting that same revision at this point.
		pool, barrier, ctx := dbtest.BarrierPool(t, d.App, func(sql string) bool {
			return strings.Contains(sql, "SELECT pref.value,pref.updated_at FROM user_preferences pref")
		})
		mux := http.NewServeMux()
		New(pool).Mount(mux)
		write := func(p tenant.Principal, body string) *httptest.ResponseRecorder {
			r := httptest.NewRequest("PUT", "/api/preferences/agents.working", strings.NewReader(body))
			r = r.WithContext(tenant.WithPrincipal(ctx, p))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			return w
		}
		type result struct {
			index int
			w     *httptest.ResponseRecorder
		}
		results := make(chan result, 2)
		go func() { results <- result{0, write(owner, bodies[0])} }()
		pid := barrier.Wait(t, ctx)
		done := make(chan struct{})
		go func() {
			results <- result{1, write(alias, bodies[1])}
			close(done)
		}()
		if lock := dbtest.BlockedOrDone(t, ctx, d.Admin, pid, done); lock != "transactionid" {
			t.Fatalf("alias save did not wait on the tenant fence: lock=%q", lock)
		}
		barrier.Release()
		winner, conflicts := -1, 0
		var winningSave preference
		for range 2 {
			r := dbtest.Await(t, ctx, results)
			switch r.w.Code {
			case 200:
				if winner != -1 {
					t.Fatal("both stale-revision saves succeeded")
				}
				winner = r.index
				must(json.Unmarshal(r.w.Body.Bytes(), &winningSave))
			case 409:
				if !strings.Contains(r.w.Body.String(), "agents plan changed; re-read before saving") {
					t.Fatalf("save conflicted for the wrong reason: %s", r.w.Body.String())
				}
				conflicts++
			default:
				t.Fatalf("save status=%d body=%s", r.w.Code, r.w.Body.String())
			}
		}
		if winner != 0 || conflicts != 1 {
			t.Fatalf("winner=%d conflicts=%d", winner, conflicts)
		}
		current := read(owner)
		if current.UpdatedAt == nil || winningSave.UpdatedAt == nil ||
			!current.UpdatedAt.After(*before.UpdatedAt) || !current.UpdatedAt.Equal(*winningSave.UpdatedAt) ||
			string(current.Value) != string(winningSave.Value) {
			t.Fatal("persisted plan does not match the winning save and new revision")
		}
		var value struct {
			Total  int               `json:"total"`
			Limits map[string]string `json:"limits"`
		}
		must(json.Unmarshal(current.Value, &value))
		if value.Total != 9+winner || value.Limits["claude"] != "off" {
			t.Fatalf("winning value changed: %+v", value)
		}
		if savedAlias := read(alias); string(savedAlias.Value) != string(current.Value) || !savedAlias.UpdatedAt.Equal(*current.UpdatedAt) {
			t.Fatal("concurrent writes left inconsistent aliases")
		}
		// Read both physical rows: the API prefers the canonical copy and
		// cannot by itself prove that the alias revision was reconciled.
		var currentValue any
		must(json.Unmarshal(current.Value, &currentValue))
		for _, p := range []tenant.Principal{owner, alias} {
			var saved []byte
			var savedValue any
			var updatedAt time.Time
			must(d.Admin.QueryRow(ctx, `SELECT value,updated_at FROM user_preferences
				WHERE tenant_id=$1 AND principal_id=$2 AND key='agents.working'`, tid, p.ID).Scan(&saved, &updatedAt))
			must(json.Unmarshal(saved, &savedValue))
			if !reflect.DeepEqual(savedValue, currentValue) || !updatedAt.Equal(*current.UpdatedAt) {
				t.Fatalf("persisted family row %s differs from the winning save", p.ID)
			}
		}
	})
	t.Run("revoked write permission is rechecked before mutation", func(t *testing.T) {
		before := read(owner)
		_, err := d.Admin.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, tid, owner.ID)
		must(err)
		assertStatus(owner, saveBody(1, before), 403)
		var saved []byte
		must(d.Admin.QueryRow(ctx, `SELECT value FROM user_preferences WHERE tenant_id=$1 AND principal_id=$2 AND key='agents.working'`, tid, owner.ID).Scan(&saved))
		var savedValue, beforeValue any
		must(json.Unmarshal(saved, &savedValue))
		must(json.Unmarshal(before.Value, &beforeValue))
		if !reflect.DeepEqual(savedValue, beforeValue) {
			t.Fatal("revoked caller changed the plan")
		}
	})
}
