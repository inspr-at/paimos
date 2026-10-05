// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/nodes"
)

// Quick creation reads vocabulary before sending a node. Exercise both requests
// through production route resolution, session authentication and authorization;
// direct node-handler calls would miss the workspace-only middleware denial.
func TestWorkVocabularyProjectOnlyCreation(t *testing.T) {
	reset(t)
	m, err := New(Config{Env: envDev, SessionKey: bytes.Repeat([]byte{7}, 32)}, appPool)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&httpapi.Server{
		Pool: appPool, Modules: []httpapi.Module{m, nodes.New(appPool, nil)},
		Middleware: []func(http.Handler) http.Handler{m.Middleware},
	}).Handler()
	request := func(method, path, body, token string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, w.Code, want, w.Body.Bytes())
		}
		if want == http.StatusForbidden {
			var denial struct {
				Code       string `json:"code"`
				ReasonCode string `json:"reason_code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &denial); err != nil {
				t.Fatal(err)
			}
			if denial.Code != "forbidden" || denial.ReasonCode != "missing_role_permission" {
				t.Fatalf("%s %s: wrong permission denial: %s", method, path, w.Body.Bytes())
			}
		}
		return w.Body.Bytes()
	}
	const path = "/api/settings/work-vocabulary"
	tid := insertTenant(t, "project-vocabulary", "Project vocabulary")
	var project, hiddenProject, workKind string
	if err := testInTenant(t.Context(), appPool, tid, func(tx pgx.Tx) error {
		for i, target := range []*string{&project, &hiddenProject} {
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title)
				SELECT $1::uuid,id,$2,$2 FROM node_kinds WHERE tenant_id=$1::uuid AND slug='project'
				RETURNING id::text`, tid, fmt.Sprintf("VOC-%d", i+1)).Scan(target); err != nil {
				return err
			}
		}
		return tx.QueryRow(t.Context(), `SELECT id::text FROM node_kinds WHERE tenant_id=$1::uuid AND slug='work'`, tid).Scan(&workKind)
	}); err != nil {
		t.Fatal(err)
	}
	projectSession := func(tenantID, projectID, role string) string {
		t.Helper()
		// The legacy Guest role cannot acquire a workspace binding at sign-in.
		id, identity := signinPerson(t, tenantID, tenantID+role, role, role+"@example.com", role+"@example.com", "guest")
		if role != "unbound" {
			if err := testInTenant(t.Context(), appPool, tenantID, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
					SELECT $1::uuid,$2::uuid,id,'project',$3::uuid FROM roles WHERE tenant_id=$1::uuid AND key=$4`, tenantID, id, projectID, role)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		token, err := m.startSession(t.Context(), identity, tenantID, id)
		if err != nil {
			t.Fatal(err)
		}
		var workspaceBindings int
		if err := adminPool.QueryRow(t.Context(), `SELECT count(*) FROM role_bindings WHERE tenant_id=$1::uuid AND principal_id=$2::uuid AND scope_type='workspace'`, tenantID, id).Scan(&workspaceBindings); err != nil {
			t.Fatal(err)
		}
		if workspaceBindings != 0 {
			t.Fatal("project-only fixture acquired a workspace binding")
		}
		return token
	}
	writer := projectSession(tid, project, "member")
	reader := projectSession(tid, project, "viewer")
	projectAdmin := projectSession(tid, project, "admin")
	unbound := projectSession(tid, project, "unbound")

	var defaults struct {
		Revision int64                         `json:"revision"`
		Leaf     struct{ Name, Icon string }   `json:"leaf"`
		Levels   []struct{ Name, Icon string } `json:"levels"`
	}
	if err := json.Unmarshal(request(http.MethodGet, path, "", writer, http.StatusOK), &defaults); err != nil {
		t.Fatal(err)
	}
	if defaults.Revision != 0 || defaults.Leaf.Name != "" || defaults.Leaf.Icon != "" || defaults.Levels == nil || len(defaults.Levels) != 0 {
		t.Fatalf("unexpected default vocabulary: %+v", defaults)
	}
	request(http.MethodGet, path, "", unbound, http.StatusForbidden)
	request(http.MethodGet, path, "", "", http.StatusUnauthorized)

	ownerID, ownerIdentity := signinPerson(t, tid, "vocabulary-owner", "Owner", "owner@example.com", "owner@example.com", "admin")
	owner, err := m.startSession(t.Context(), ownerIdentity, tid, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	const vocabulary = `{"revision":0,"leaf":{"name":"Arbeitsschritt","icon":"check"},"levels":[{"name":"Vorhaben","icon":"tree"}]}`
	saved := request(http.MethodPut, path, vocabulary, owner, http.StatusOK)
	for _, token := range []string{writer, reader, projectAdmin} {
		if got := request(http.MethodGet, path, "", token, http.StatusOK); !bytes.Equal(got, saved) {
			t.Fatalf("project reader did not receive saved vocabulary: %s", got)
		}
		// Even a project Admin cannot turn this read admission into settings.manage.
		request(http.MethodPut, path, vocabulary, token, http.StatusForbidden)
	}
	if got := request(http.MethodGet, path, "", owner, http.StatusOK); !bytes.Equal(got, saved) {
		t.Fatal("denied project writes changed vocabulary")
	}
	create := fmt.Sprintf(`{"kind_id":%q,"parent_id":%q,"title":"Project-only quick creation","state":"new","fields":{}}`, workKind, project)
	created := request(http.MethodPost, "/api/nodes", create, writer, http.StatusCreated)
	var node struct {
		ID       string `json:"id"`
		KindID   string `json:"kind_id"`
		ParentID string `json:"parent_id"`
		Title    string `json:"title"`
	}
	if err := json.Unmarshal(created, &node); err != nil {
		t.Fatal(err)
	}
	if node.ID == "" || node.KindID != workKind || node.ParentID != project || node.Title != "Project-only quick creation" {
		t.Fatalf("unexpected created work: %s", created)
	}
	request(http.MethodGet, "/api/nodes/"+node.ID, "", writer, http.StatusOK)
	request(http.MethodPost, "/api/nodes", create, reader, http.StatusForbidden)
	denied := request(http.MethodPost, "/api/nodes", strings.Replace(create, project, hiddenProject, 1), writer, http.StatusBadRequest)
	if !bytes.Contains(denied, []byte("parent node does not exist or is deleted")) {
		t.Fatalf("wrong invisible-project denial: %s", denied)
	}
	var persistedProject string
	var workCount int
	if err := adminPool.QueryRow(t.Context(), `SELECT project_id::text,
		(SELECT count(*) FROM nodes WHERE tenant_id=$1::uuid AND kind_id=$3::uuid)
		FROM nodes WHERE tenant_id=$1::uuid AND id=$2::uuid`, tid, node.ID, workKind).Scan(&persistedProject, &workCount); err != nil {
		t.Fatal(err)
	}
	if persistedProject != project || workCount != 1 {
		t.Fatalf("work creation did not stay in its authorized project or a denied request wrote work: project=%s count=%d", persistedProject, workCount)
	}

	// Tenant configuration stays tenant-scoped even with an any-project grant.
	otherTenant := insertTenant(t, "other-vocabulary", "Other vocabulary")
	var otherProject string
	if err := testInTenant(t.Context(), appPool, otherTenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title)
			SELECT $1::uuid,id,'OTHER-1','Other' FROM node_kinds WHERE tenant_id=$1::uuid AND slug='project'
			RETURNING id::text`, otherTenant).Scan(&otherProject)
	}); err != nil {
		t.Fatal(err)
	}
	other := projectSession(otherTenant, otherProject, "member")
	var otherDefaults struct {
		Revision int64                 `json:"revision"`
		Leaf     struct{ Name string } `json:"leaf"`
	}
	if err := json.Unmarshal(request(http.MethodGet, path, "", other, http.StatusOK), &otherDefaults); err != nil {
		t.Fatal(err)
	}
	if otherDefaults.Revision != 0 || otherDefaults.Leaf.Name != "" {
		t.Fatal("project reader received another tenant's vocabulary")
	}
}
