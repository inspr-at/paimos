// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestRecurrenceAgentScopeIsExplicitAndBounded(t *testing.T) {
	base := "/api/recurrences"
	id := base + "/57000000-0000-4000-8000-000000000001"
	for _, tc := range []struct{ method, path string }{
		{"GET", base}, {"POST", base}, {"POST", base + "/preview"},
		{"GET", base + "/templates"}, {"HEAD", base + "/templates"},
		{"POST", base + "/templates/weekly-cleanup/1/preview"}, {"POST", base + "/templates/weekly-cleanup/1/apply"},
		{"GET", id}, {"PUT", id}, {"DELETE", id}, {"HEAD", id + "/history"},
		{"GET", id + "/preview"}, {"GET", id + "/history"}, {"GET", id + "/releases"}, {"GET", id + "/guardrails"},
		{"POST", id + "/pause"}, {"POST", id + "/resume"}, {"POST", id + "/run-now"},
	} {
		if got, ok := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil)); !ok || got != "recurrences.manage" {
			t.Errorf("%s %s: %q %v", tc.method, tc.path, got, ok)
		}
	}
	for _, tc := range []struct{ method, path string }{
		{"PUT", id + "/guardrails"}, {"DELETE", base}, {"PATCH", id}, {"POST", id + "/history"},
		{"POST", base + "/templates"}, {"PUT", base + "/templates"}, {"GET", base + "/templates/weekly-cleanup/1/apply"},
		{"POST", base + "/templates/weekly-cleanup/1/resume"}, {"POST", base + "/templates/weekly-cleanup/1/apply/extra"},
		{"GET", id + "/secrets"}, {"POST", id + "/unknown"}, {"GET", id + "/history/private"},
	} {
		if got, ok := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil)); ok || got != "" {
			t.Errorf("unexpected agent route %s %s: %q %v", tc.method, tc.path, got, ok)
		}
	}
}

// Risk: full-access keys cannot schedule work in writable projects, or deriving
// recurrence grants bypasses project, scoped-key, creator or acting-person limits.
func TestFullAccessRecurrencesFollowProjectWriteGrants(t *testing.T) {
	m, owner := keyFixture(t)
	full := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "recurrence-worker", "full_access": true}))
	scoped := decodeKey(t, keyRequest(m, owner, map[string]any{
		"principal_id": full.PrincipalID, "scopes": []string{"nodes.read", "nodes.write", recurrences.Permission},
	}))
	ctx := dbtest.Seed(t.Context())
	tx := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, m.pool, owner.TenantID, fn); err != nil {
			t.Fatal(err)
		}
	}
	projects := []struct {
		name, role, id, parent, recurrence string
	}{
		{name: "AEON", role: "explicit"},
		{name: "HNET", role: "admin"},
		{name: "AIT", role: "member"},
		{name: "JOED", role: "recurrence_writer"},
		{name: "OPS", role: "recurrence_writer"},
		{name: "START", role: "recurrence_writer"},
		{name: "READONLY", role: "viewer"},
		{name: "HIDDEN"},
	}
	var writer, explicit string
	tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT role_id::text FROM role_bindings WHERE principal_id=$1 AND scope_type='workspace'`, full.PrincipalID).Scan(&explicit); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, full.PrincipalID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'recurrence_writer','Project writer') RETURNING id::text`, owner.TenantID).Scan(&writer); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read'),($1,$2,'nodes.write')`, owner.TenantID, writer); err != nil {
			return err
		}
		for i := range projects {
			project := &projects[i]
			if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,$2,$3 FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, owner.TenantID, project.name+"-1", project.name).Scan(&project.id); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,$2,'Weekly work',$3 FROM node_kinds WHERE tenant_id=$1 AND slug='work' RETURNING id::text`, owner.TenantID, project.name+"-2", project.id).Scan(&project.parent); err != nil {
				return err
			}
			if project.role == "" {
				continue
			}
			role := explicit
			if project.role != "explicit" {
				if err := tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE tenant_id=$1 AND key=$2`, owner.TenantID, project.role).Scan(&role); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, owner.TenantID, full.PrincipalID, role, project.id); err != nil {
				return err
			}
		}
		return nil
	})
	mux := http.NewServeMux()
	recurrences.New(m.pool).Mount(mux)
	handler := m.Middleware(mux)
	call := func(t *testing.T, key *agentKeyCreatedJSON, method, path string, body any, want int) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		w := httptest.NewRecorder()
		if key == nil {
			mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), owner)))
		} else {
			r.Header.Set("Authorization", "Bearer "+key.Token)
			_, r.Pattern = mux.Handler(r)
			handler.ServeHTTP(w, r)
		}
		if w.Code != want {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w.Body.Bytes()
	}
	input := func(i int) recurrences.Input {
		return recurrences.Input{
			ProjectID: projects[i].id, ParentID: projects[i].parent,
			Template: recurrences.Template{Title: "Weekly project check", EstimateHours: 1},
			Trigger:  recurrences.Trigger{Kind: "time", RRULE: "FREQ=WEEKLY;BYDAY=TU", TimeOfDay: "09:00", Timezone: "Europe/Vienna"},
		}
	}
	list := func(t *testing.T, key *agentKeyCreatedJSON, project string, want map[string]bool) {
		t.Helper()
		path := "/api/recurrences"
		if project != "" {
			path += "?project_id=" + project
		}
		var page struct {
			Items []recurrences.Recurrence `json:"items"`
			Next  *string                  `json:"next_cursor"`
		}
		if err := json.Unmarshal(call(t, key, "GET", path, nil, http.StatusOK), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != len(want) || page.Next != nil {
			t.Fatalf("list returned %d rows, want %d; next=%v", len(page.Items), len(want), page.Next)
		}
		seen := map[string]bool{}
		for _, item := range page.Items {
			if !want[item.ID] || seen[item.ID] {
				t.Fatalf("unexpected or duplicate recurrence %s", item.ID)
			}
			seen[item.ID] = true
		}
	}
	writable := map[string]bool{}
	for i := range projects {
		t.Run(projects[i].name, func(t *testing.T) {
			key := &full
			allowed := projects[i].role != "" && projects[i].role != "viewer"
			if !allowed {
				call(t, &full, "POST", "/api/recurrences", input(i), http.StatusForbidden)
				key = nil // Retain a real inaccessible row to test list filtering.
			}
			var item recurrences.Recurrence
			if err := json.Unmarshal(call(t, key, "POST", "/api/recurrences", input(i), http.StatusCreated), &item); err != nil {
				t.Fatal(err)
			}
			if item.ID == "" || item.ProjectID != projects[i].id {
				t.Fatal("created recurrence lost its target project")
			}
			projects[i].recurrence = item.ID
			want := map[string]bool{}
			if allowed {
				writable[item.ID], want[item.ID] = true, true
			}
			list(t, &full, projects[i].id, want)
		})
	}
	list(t, &full, "", writable)
	// A scoped key with recurrences.manage still needs an explicit role grant.
	call(t, &scoped, "POST", "/api/recurrences", input(1), http.StatusForbidden)
	list(t, &scoped, "", map[string]bool{projects[0].recurrence: true})
	prefix, secret, _ := parseBearer("Bearer " + full.Token)
	p, ok, err := m.authenticateAgent(ctx, prefix, secret)
	if err != nil || !ok {
		t.Fatal("full-access fixture did not authenticate")
	}
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		check, err := authz.ProjectsTx(t.Context(), tx, p)
		if err != nil {
			return err
		}
		for _, project := range projects {
			allowed := writable[project.recurrence]
			if check(recurrences.Permission, project.id) != allowed {
				return errors.New("bulk recurrence grant differs in " + project.name)
			}
			err := authz.RequireTx(t.Context(), tx, p, recurrences.Permission, authz.Scope{ProjectID: project.id})
			if allowed && err != nil || !allowed && !errors.Is(err, authz.ErrForbidden) {
				return errors.New("single recurrence grant differs in " + project.name)
			}
		}
		if check(recurrences.Permission, "") {
			return errors.New("project grant became workspace authority")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE role_id=$1 AND permission='nodes.write'`, writer)
		return err
	})
	for i := range projects {
		if projects[i].role == "recurrence_writer" {
			call(t, &full, "POST", "/api/recurrences", input(i), http.StatusForbidden)
			list(t, &full, projects[i].id, nil)
			delete(writable, projects[i].recurrence)
		}
	}
	list(t, &full, "", writable)
	// Even a writable agent role cannot replace its creator/person's explicit
	// recurrence authority. All mutations here are isolated fixture data.
	limited := keyScopeEditor(t, m, owner, "nodes.read", "nodes.write")
	for _, ceiling := range []string{"creator", "person"} {
		tx(func(tx pgx.Tx) error {
			if ceiling == "creator" {
				_, err := tx.Exec(ctx, `UPDATE agent_keys SET created_by_principal_id=$2 WHERE id=$1`, full.ID, limited.ID)
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE agent_keys SET created_by_principal_id=$2,person_id=$3 WHERE id=$1`, full.ID, owner.ID, limited.ID)
			return err
		})
		call(t, &full, "POST", "/api/recurrences", input(1), http.StatusForbidden)
		call(t, &full, "GET", "/api/recurrences", nil, http.StatusForbidden)
	}
	// A workspace writer still sees only the projects its creator may manage.
	tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.write')`, owner.TenantID, writer); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, owner.TenantID, full.PrincipalID, writer); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, limited.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, owner.TenantID, limited.ID, projects[1].id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE agent_keys SET created_by_principal_id=$2,person_id=NULL WHERE id=$1`, full.ID, limited.ID)
		return err
	})
	list(t, &full, "", map[string]bool{projects[1].recurrence: true})
	call(t, &full, "POST", "/api/recurrences", input(2), http.StatusForbidden)
	call(t, &full, "POST", "/api/recurrences", input(1), http.StatusCreated)
}
